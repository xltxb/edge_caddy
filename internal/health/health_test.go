package health_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/health"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/ws"
)

type recorder struct {
	mu     sync.Mutex
	frames []ws.Frame
}

func (r *recorder) Broadcast(f ws.Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, f)
}

func (r *recorder) events() []ws.Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ws.Frame, 0, len(r.frames))
	for _, f := range r.frames {
		if f.Type == "event" {
			out = append(out, f)
		}
	}
	return out
}

func newChecker(t *testing.T, now func() time.Time) (*health.Checker, *store.Store, *recorder) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	rec := &recorder{}
	c := health.New(st, rec, health.Config{
		Interval: 3 * time.Second, MissedBeats: 3, Now: now,
	})
	return c, st, rec
}

// 连续错过 N 次心跳判定离线，并发一条事件。
func TestNodeGoesDownAfterMissedBeats(t *testing.T) {
	base := time.Now()
	now := base
	c, st, rec := newChecker(t, func() time.Time { return now })
	ctx := context.Background()

	if err := st.UpsertNodeSeen(ctx, "node-a", "cfg-1", base); err != nil {
		t.Fatal(err)
	}

	// 还没超时
	now = base.Add(5 * time.Second)
	c.Sweep(ctx)
	nodes, _ := st.ListNodes(ctx)
	if nodes[0].Status != "ok" {
		t.Fatalf("未超时不应判离线，实际 %s", nodes[0].Status)
	}

	// 超过 3×3s
	now = base.Add(10 * time.Second)
	c.Sweep(ctx)
	nodes, _ = st.ListNodes(ctx)
	if nodes[0].Status != "down" {
		t.Fatalf("连续错过 3 次心跳应判离线，实际 %s", nodes[0].Status)
	}
	if len(rec.events()) != 1 {
		t.Fatalf("应发出 1 条离线事件，实际 %d", len(rec.events()))
	}
}

// 已经离线的节点**不再重复发事件**。
//
// 巡检每 3 秒跑一次；重复发的话，一个掉了一夜的节点能在事件流里刷出上万条，
// 把真正需要注意的东西全挤掉——事件流也就没用了。
func TestOfflineNodeDoesNotSpamEvents(t *testing.T) {
	base := time.Now()
	now := base
	c, st, rec := newChecker(t, func() time.Time { return now })
	ctx := context.Background()
	_ = st.UpsertNodeSeen(ctx, "node-a", "cfg-1", base)

	now = base.Add(10 * time.Second)
	for i := 0; i < 20; i++ {
		now = now.Add(3 * time.Second)
		c.Sweep(ctx)
	}
	if n := len(rec.events()); n != 1 {
		t.Fatalf("持续离线只应发 1 条事件，实际 %d 条", n)
	}
}

// 恢复时发一条恢复事件，且再次掉线还能再报。
func TestRecoveryEmitsEventAndRearms(t *testing.T) {
	base := time.Now()
	now := base
	c, st, rec := newChecker(t, func() time.Time { return now })
	ctx := context.Background()
	_ = st.UpsertNodeSeen(ctx, "node-a", "cfg-1", base)

	now = base.Add(10 * time.Second)
	c.Sweep(ctx) // 掉线

	// 心跳回来了
	_ = st.UpsertNodeSeen(ctx, "node-a", "cfg-1", now)
	c.Sweep(ctx)
	nodes, _ := st.ListNodes(ctx)
	if nodes[0].Status != "ok" {
		t.Fatalf("心跳恢复后应回到 ok，实际 %s", nodes[0].Status)
	}
	if n := len(rec.events()); n != 2 {
		t.Fatalf("应有离线与恢复各 1 条事件，实际 %d 条", n)
	}

	// 再掉一次还要能报——否则「掉线→恢复→再掉线」的第二次会静默
	now = now.Add(20 * time.Second)
	c.Sweep(ctx)
	if n := len(rec.events()); n != 3 {
		t.Fatalf("再次掉线应再报一次，实际共 %d 条", n)
	}
}

// 从未上报过心跳的节点不该被判成「掉线」。
//
// 它是「还没接入」，不是「掉了」。混为一谈会让刚签发 Token 还没装的节点
// 立刻触发一条离线告警。
func TestNeverSeenNodeIsNotReportedAsDown(t *testing.T) {
	base := time.Now()
	now := base.Add(time.Hour)
	c, st, rec := newChecker(t, func() time.Time { return now })
	ctx := context.Background()

	// 直接插一条没有心跳时间的节点
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO edge_nodes (id, status, cfg_version, created_at) VALUES ('node-new','down','',?)`,
		base.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	c.Sweep(ctx)
	if n := len(rec.events()); n != 0 {
		t.Fatalf("从未接入的节点不该产生离线事件，实际 %d 条", n)
	}
}
