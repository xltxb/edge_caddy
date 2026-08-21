package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/xltxb/edge_caddy/internal/store"
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

// **心跳不能冲掉下线标记。**
//
// 这条钉的是 [ADR-0014] 的核心论据本身。那份 ADR 的全部理由是：`status` 已经
// 有两个自动写入方（心跳写 ok/warn，health 写 down），把「已下线」塞进同一列
// 就会被心跳冲掉。据此决定新增一列。
//
// 而分了列之后，**没有任何东西证明那个效果真的达成了**——它成立只是因为
// 写 TouchHeartbeat 那条 SQL 的人碰巧没写 drained_at 这一列。
//
// 这个缺口不会被现有测试发现：已下线的节点被拒绝接入，所以在 e2e 里根本不会
// 有心跳打进来，那条路径走不到。谁哪天给 TouchHeartbeat 补一句
// 「节点回来了就清掉下线标记」——听起来完全合理——下线会静默失效，而全部
// 测试照旧全绿。
//
// 前端在他们的 mock 夹具里钉住了同一条，理由说得比我准：
// **一个夹具表达不了的状态，等于在开发期不存在。**
func TestHeartbeatDoesNotClearDrainedMark(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()

	if err := s.UpsertNode(ctx, store.NodeSpec{
		NodeID: "node-a", City: "香港", Vendor: "DMIT", Line: "CN2 GIA",
		PublicIP: "203.0.113.7",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeDrained(ctx, "node-a", true); err != nil {
		t.Fatal(err)
	}

	// 心跳照常打。这在现实里是可能的：下线与断开之间有一个窗口，
	// 而且这条路径将来还会被别的改动碰到。
	if err := s.TouchHeartbeat(ctx, "node-a", "cfg-1", "ok"); err != nil {
		t.Fatal(err)
	}

	nodes, err := s.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("应当有一个节点，实际 %d", len(nodes))
	}
	n := nodes[0]
	if n.DrainedAt == nil {
		t.Error("心跳把下线标记冲掉了 —— 这正是 ADR-0014 分两列要防的事")
	}
	// 「已下线且在线」是一个**合法**的组合，不是矛盾态：刚按下下线，
	// 隧道还没断干净。status 该照实说它此刻是健康的。
	if n.Status != "ok" {
		t.Errorf("status = %q，心跳到达时它就该是 ok —— 两个事实各说各的", n.Status)
	}
	drained, err := s.IsNodeDrained(ctx, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if !drained {
		t.Error("IsNodeDrained 应当仍然为真")
	}
}

// 反向：health 判离线也不该动下线标记，两个方向都要不互相覆盖。
func TestMarkingDownDoesNotClearDrainedMark(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()

	if err := s.UpsertNode(ctx, store.NodeSpec{
		NodeID: "node-a", City: "香港", Vendor: "DMIT", Line: "CN2 GIA",
		PublicIP: "203.0.113.7",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeDrained(ctx, "node-a", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeDown(ctx, "node-a"); err != nil {
		t.Fatal(err)
	}

	drained, err := s.IsNodeDrained(ctx, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if !drained {
		t.Error("判离线把下线标记冲掉了 —— 那会让「重新上线」按钮无事可做")
	}
}

// **查验不消耗，接入成功才消耗。**
//
// 查验与消耗一体的时候，查验之后那几件会失败的事——节点已被下线、写入节点失败、
// 签发隧道证书失败——**每一件失败都会把 Token 烧掉**。而后两件是主控自己的
// 内部错误：凭什么让用户的 Token 作废？人这时要回控制台重签一张，
// 而错误只出现在那台机器的日志里。
//
// 这条是照前端 agent 那个办法找出来的：从**名字**推，不从实现推。
// 「已下线的节点被拒绝接入直到重新上线」——那就构造一个 rejoin 之后的情形，
// 结果发现那张下线前签好的 Token 已经废了。
func TestPeekDoesNotConsumeButConsumeDoes(t *testing.T) {
	s := testdb.New(t)
	ctx := context.Background()

	spec := store.NodeSpec{
		NodeID: "node-a", City: "香港", Vendor: "DMIT", Line: "CN2 GIA",
		PublicIP: "203.0.113.7",
	}
	plain, _, err := s.IssueEnrollToken(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}

	// 查验三次，每次都该成功——查验本身不该改变任何东西。
	for i := range 3 {
		got, err := s.PeekEnrollToken(ctx, plain)
		if err != nil {
			t.Fatalf("第 %d 次查验失败：%v —— 查验不该消耗它", i+1, err)
		}
		if got.NodeID != "node-a" {
			t.Fatalf("查验回的 node_id = %q", got.NodeID)
		}
	}

	if err := s.ConsumeEnrollToken(ctx, plain); err != nil {
		t.Fatalf("消耗失败：%v", err)
	}
	// 消耗之后就不能再用了 —— 一次性这个性质由 Consume 承担，不由 Peek。
	if err := s.ConsumeEnrollToken(ctx, plain); !errors.Is(err, store.ErrTokenUsed) {
		t.Errorf("重复消耗应当报 ErrTokenUsed，实际 %v", err)
	}
	if _, err := s.PeekEnrollToken(ctx, plain); !errors.Is(err, store.ErrTokenUsed) {
		t.Errorf("已用过的 Token 查验也该报 ErrTokenUsed，实际 %v", err)
	}
}
