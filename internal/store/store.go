// Package store 是主控的持久化层：一个 pgxpool 加一组按表拆分的仓储方法。
//
// 存储是 PostgreSQL 16，不是 SQLite——见
// docs/adr/0011-postgres-supersedes-sqlite.md（取代 ADR-0006）。
// 那条 ADR 同时定下：仓储层单测直连本机 PG，不做内存假实现，
// 因为假实现正是「未验证的生产路径」的另一种形式。
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 迁移文件放在本包目录下而不是仓库根的 migrations/（后端开发文档 §2 画的是后者）：
// Go 的 embed 够不到父目录，放根上就只能靠构建时拷贝，而那意味着仓库里会同时存在
// 两份 schema，改错一份不会有任何症状。这里只有一份，它就是被编译进二进制的那份。
//
//go:embed all:migrations
var migrationFS embed.FS

type Store struct {
	Pool *pgxpool.Pool
	url  string
}

func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("解析数据库地址: %w", err)
	}
	// ADR-0011：主控是单进程单写入者，6 个节点、几百条路由。10 条连接足够，
	// 开大只会在 PG 侧多占后端进程。
	cfg.MaxConns = 10

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("建立连接池: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("连接数据库: %w", err)
	}
	return &Store{Pool: pool, url: url}, nil
}

func (s *Store) Close() { s.Pool.Close() }

// Migrate 把库迁到最新。迁移在进程内跑（后端文档 §8 的 master -migrate），
// 不依赖 migrate CLI——少一个必须与二进制同时分发的东西。
func (s *Store) Migrate() error {
	src, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("加载迁移文件: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, migrateURL(s.url))
	if err != nil {
		return fmt.Errorf("初始化迁移: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("执行迁移: %w", err)
	}
	return nil
}

// migrateURL 把 pgx 的连接串换成 golang-migrate 的 pgx/v5 驱动 scheme。
// 两者用的是同一个 DSN，只是 scheme 名字不同。
func migrateURL(url string) string {
	for _, p := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(url, p) {
			return "pgx5://" + strings.TrimPrefix(url, p)
		}
	}
	return url
}
