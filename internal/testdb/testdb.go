// Package testdb 给测试开一次性的真数据库。
//
// 它存在的理由是 docs/adr/0011-postgres-supersedes-sqlite.md：仓储层与 API 层
// 的测试直连本机 PostgreSQL，不做内存假实现——假实现正是那条 ADR 想避免的
// 「未验证的生产路径」的另一种形式。
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xltxb/edge_caddy/internal/store"
)

// adminURL 指向一个用来建库删库的库（默认 postgres）。
// 这里不接受「没有真库就跳过测试」——ADR-0011 的整个论点就是
// 「本机跑的就是线上跑的」，一个会自动跳过的测试等于没有测试。
func adminURL() string {
	if v := strings.TrimSpace(os.Getenv("EC_TEST_ADMIN_URL")); v != "" {
		return v
	}
	return "postgres://localhost:5432/postgres?sslmode=disable"
}

// newTestStore 为每个测试开一个独立的库并迁到最新，测试结束后删掉。
//
// 独立库而不是共享库 + 事务回滚：迁移本身要建 TYPE 和 TABLE，
// 而我们恰恰想测迁移。共享库里跑迁移会让并行的测试互相看见对方的 DDL。
func New(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()

	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("生成库名: %v", err)
	}
	dbName := "ec_test_" + hex.EncodeToString(b[:])

	admin, err := pgx.Connect(ctx, adminURL())
	if err != nil {
		t.Fatalf("连接管理库失败（%s）：%v\n"+
			"仓储层测试要求本机有可用的 PostgreSQL——见 docs/adr/0011-postgres-supersedes-sqlite.md", adminURL(), err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		admin.Close(ctx)
		t.Fatalf("建库 %s: %v", dbName, err)
	}
	admin.Close(ctx)

	url := replaceDBName(adminURL(), dbName)
	s, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("打开 %s: %v", dbName, err)
	}
	if err := s.Migrate(); err != nil {
		s.Close()
		t.Fatalf("迁移 %s: %v", dbName, err)
	}

	t.Cleanup(func() {
		s.Close()
		// 连接池关闭是异步的，PG 可能还认为有活动连接。给它一点时间，
		// 删不掉就强制断开——留一个 ec_test_* 库比让测试失败更糟，
		// 因为下次跑测试的人会看到一堆垃圾库而不知道从哪来。
		ctx := context.Background()
		a, err := pgx.Connect(ctx, adminURL())
		if err != nil {
			return
		}
		defer a.Close(ctx)
		for i := 0; i < 20; i++ {
			if _, err := a.Exec(ctx, "DROP DATABASE "+dbName); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		_, _ = a.Exec(ctx, fmt.Sprintf(
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'", dbName))
		_, _ = a.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName)
	})
	return s
}

func replaceDBName(url, name string) string {
	i := strings.LastIndex(url, "/")
	j := strings.Index(url[i:], "?")
	if j < 0 {
		return url[:i+1] + name
	}
	return url[:i+1] + name + url[i+j:]
}
