// Package store 是主控的持久化层。
//
// 只用 SQLite（docs/adr/0006）：主控是单进程单写入者，数据量是 6 个节点、
// 几百条路由和一条审计流水，SQLite 绰绰有余；更重要的是开发跑的就是生产跑的，
// 不存在「本地全绿、线上未知」的第二条路径。
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 实现，不需要 cgo
)

//go:embed schema.sql
var schemaSQL string

// ErrNotFound 表示目标记录不存在。
//
// 调用方据此区分「查无此物」与「查询失败」——两者的处理完全不同：
// 前者可能是 404，后者是 500。混在一起会让「删错了 ID」看起来像成功。
var ErrNotFound = errors.New("记录不存在")

type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）数据库并建表。
func Open(path string) (*Store, error) {
	// WAL 必须显式开：默认的回滚日志模式下读会阻塞写，而心跳写入与页面查询
	// 本来就是并发的。busy_timeout 让偶发的锁竞争等待而不是立刻报错。
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	// 单连接写入：SQLite 的写是串行的，放开连接数只会把锁竞争推迟到更难排查的地方。
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(context.Background(), schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化表结构: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层句柄，仅供需要自行开事务的调用方（如下发编排）使用。
func (s *Store) DB() *sql.DB { return s.db }

const timeFmt = time.RFC3339Nano

func encodeTime(t time.Time) string { return t.UTC().Format(timeFmt) }

func decodeTime(s string) time.Time {
	t, err := time.Parse(timeFmt, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func encodeJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("序列化: %w", err)
	}
	return string(b), nil
}

func decodeJSON(s string, out any) error {
	if s == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(s), out); err != nil {
		return fmt.Errorf("反序列化 %q: %w", truncate(s, 60), err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
