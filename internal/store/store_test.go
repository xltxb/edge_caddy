package store_test

import (
	"context"
	"testing"

	"github.com/xltxb/edge_caddy/internal/testdb"
)

// TestMigrateCreatesSchema 是这一层最基础的断言：迁移能在一个空库上跑到底。
// 它同时是 ADR-0011 兑现的地方——跑的是真 PostgreSQL 16，不是内存替身。
func TestMigrateCreatesSchema(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()

	wantTables := []string{
		"users", "sessions", "edge_nodes", "enroll_tokens",
		"proxy_routes", "access_rules", "global_policies", "config_drafts",
		"deploys", "deploy_results", "baseline", "dns_weights",
		"certs", "cert_nodes", "pki_cas",
		"events", "traffic_samples", "audit_logs", "settings",
	}
	for _, tbl := range wantTables {
		var ok bool
		err := s.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			                WHERE table_schema='public' AND table_name=$1)`, tbl).Scan(&ok)
		if err != nil {
			t.Fatalf("查表 %s: %v", tbl, err)
		}
		if !ok {
			t.Errorf("表 %s 不存在", tbl)
		}
	}

	wantEnums := []string{"node_status", "block_mode", "rule_type", "deploy_state", "op_result", "event_kind"}
	for _, e := range wantEnums {
		var ok bool
		if err := s.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_type WHERE typname=$1)`, e).Scan(&ok); err != nil {
			t.Fatalf("查枚举 %s: %v", e, err)
		}
		if !ok {
			t.Errorf("枚举 %s 不存在", e)
		}
	}
}

// TestMigrateIsIdempotent —— master -migrate 会在每次发布时跑，
// 跑第二遍必须是无操作而不是报错。
func TestMigrateIsIdempotent(t *testing.T) {
	s := testdb.New(t)
	if err := s.Migrate(); err != nil {
		t.Fatalf("第二次迁移应当无操作，却报错: %v", err)
	}
}

// TestEventKindHasOk 锁住那个差点被合并掉的枚举值。
// ok 与 info 的分工是事件流的设计意图（api-contract §2）：
// ok = 成功完成的动作，info = 流水账。合并会让下发成功和背景噪音同色。
func TestEventKindHasOk(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()

	var vals []string
	rows, err := s.Pool.Query(ctx,
		`SELECT e.enumlabel FROM pg_enum e JOIN pg_type t ON t.oid = e.enumtypid
		 WHERE t.typname = 'event_kind' ORDER BY e.enumsortorder`)
	if err != nil {
		t.Fatalf("读 event_kind: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		vals = append(vals, v)
	}
	want := []string{"ok", "info", "warn", "crit"}
	if len(vals) != len(want) {
		t.Fatalf("event_kind = %v，想要 %v", vals, want)
	}
	for i := range want {
		if vals[i] != want[i] {
			t.Errorf("event_kind[%d] = %q，想要 %q", i, vals[i], want[i])
		}
	}
}

// TestBaselineIsSingleRow —— 基线是「当前应当在所有边缘节点上生效的那一版」，
// 按定义只能有一行。两行基线意味着系统对「现在应该是哪版」有两个答案。
func TestBaselineIsSingleRow(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()

	if _, err := s.Pool.Exec(ctx,
		`INSERT INTO baseline (cfg_version) VALUES ('cfg-aaa')`); err != nil {
		t.Fatalf("插入第一行基线: %v", err)
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO baseline (cfg_version) VALUES ('cfg-bbb')`)
	if err == nil {
		t.Fatal("插入第二行基线本应被拒绝，却成功了")
	}
}
