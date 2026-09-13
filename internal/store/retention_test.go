package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
)

// TestPruneKeepsWhatIsStillUseful：三张只写不清的表要有清理路径。
//
// sessions / events / audit_logs 原先没有任何删除路径，行数随时间线性涨
// （issue #34）。它不是「磁盘会满」那么简单：
//
//   - events 上每台节点一次全表扫（#59 修的那个）随行数一起变慢
//   - 审计页的第一页被日常写操作填满（#49 说的那件事）
//   - sessions 里堆着早就过期的行，而每次请求都要查它
//
// **三张表的判据不同**，这正是它们不能共用一个「保留 N 天」的原因：
//
//   - sessions 删的是**已经过期的**（expires_at 在过去），与保留期无关——
//     一条过期会话没有任何价值，多留一天也不会有人去看
//   - events / audit_logs 按保留期删，而审计是 ADR-0013 准入模型的三分之一，
//     保留期要比事件长
func TestPruneKeepsWhatIsStillUseful(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()

	// --- sessions：过期的该走，没过期的要留 ---
	if err := st.CreateUser(ctx, "abiu", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	live, err := st.CreateSession(ctx, "abiu", "203.0.113.7", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dead, err := st.CreateSession(ctx, "abiu", "203.0.113.7", -time.Hour) // 已经过期
	if err != nil {
		t.Fatal(err)
	}

	// --- events / audit：造一条老的、一条新的 ---
	if _, err := st.InsertEvent(ctx, "node-a", "warn", "很久以前"); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertAudit(ctx, store.AuditRecord{
		Operator: "abiu", Action: "下发配置", Target: "route:a", Result: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	// 把它们的时间拨回 400 天前 —— 比任何合理的保留期都久。
	for _, tbl := range []string{"events", "audit_logs"} {
		if _, err := st.Pool.Exec(ctx,
			`UPDATE `+tbl+` SET created_at = now() - interval '400 days'`); err != nil {
			t.Fatal(err)
		}
	}
	// 再各造一条「刚刚」的。
	if _, err := st.InsertEvent(ctx, "node-a", "ok", "刚刚"); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertAudit(ctx, store.AuditRecord{
		Operator: "abiu", Action: "下发配置", Target: "route:b", Result: "ok",
	}); err != nil {
		t.Fatal(err)
	}

	if err := st.Prune(ctx); err != nil {
		t.Fatal(err)
	}

	// 没过期的会话还在，过期的走了。
	if _, err := st.SessionOwner(ctx, live); err != nil {
		t.Errorf("没过期的会话被清掉了 —— 人会在使用中途被踢出去：%v", err)
	}
	if _, err := st.SessionOwner(ctx, dead); err == nil {
		t.Error("过期的会话还在 —— 而每次请求都要查这张表")
	}

	count := func(tbl string) int {
		var n int
		if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM `+tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("events"); n != 1 {
		t.Errorf("events 剩 %d 行，想要 1（清掉 400 天前那条，留下刚刚那条）", n)
	}
	if n := count("audit_logs"); n != 1 {
		t.Errorf("audit_logs 剩 %d 行，想要 1", n)
	}
}
