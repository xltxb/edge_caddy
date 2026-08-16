package deploy_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/deploy"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/store"
)

func rollbackRig(t *testing.T) (*deploy.Orchestrator, *store.Store, *fakeTunnel) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "r.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tun := &fakeTunnel{nodes: []string{"node-a"}, sends: map[string]int{}}
	tun.reply = func(_ string, _ int, cfg string) *edgev1.PushResult {
		return &edgev1.PushResult{CfgVersion: cfg, Ok: true, Detail: "31ms"}
	}
	o := deploy.NewWithRetry(st, tun, deploy.RetryPolicy{Max: 1, Deadline: time.Second}, nil)
	tun.orch = o
	return o, st, tun
}

// 回滚把该版本的资源状态写回**草稿**，不直接推送（PRD §6.3）。
//
// 直接推送等于绕过了校验与人工确认——而回滚往往发生在出事的时候，
// 正是最需要有人看一眼 diff 的时刻。
func TestRollbackWritesDraftsInsteadOfPushing(t *testing.T) {
	o, st, tun := rollbackRig(t)
	ctx := context.Background()

	if err := st.PutRoute(ctx, model.Route{
		Domain: "api.example.com", Upstream: "10.0.0.1:80",
		Block: model.BlockAbort, BodyMax: "5MB",
	}); err != nil {
		t.Fatal(err)
	}
	v1, err := o.Deploy(ctx, "abiu", nil)
	if err != nil {
		t.Fatal(err)
	}

	// 改了再推一版
	_ = st.PutRoute(ctx, model.Route{
		Domain: "api.example.com", Upstream: "10.0.0.9:9090",
		Block: model.Block403, BodyMax: "64MB",
	})
	if _, err := o.Deploy(ctx, "abiu", nil); err != nil {
		t.Fatal(err)
	}
	sendsBefore := tun.count("node-a")

	// 回滚到 v1
	keys, err := o.Rollback(ctx, v1.CfgVersion, "abiu")
	if err != nil {
		t.Fatalf("回滚失败: %v", err)
	}

	// 不得触达任何节点
	if tun.count("node-a") != sendsBefore {
		t.Fatalf("回滚不应直接推送，实际又发了 %d 次", tun.count("node-a")-sendsBefore)
	}
	if len(keys) != 1 || keys[0] != "route:api.example.com" {
		t.Fatalf("回滚应返回写回的资源键，实际 %v", keys)
	}

	// 草稿里应是 v1 的值，且**线上值未被改动**——推送前一切照旧
	ds, err := st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 {
		t.Fatalf("应写回 1 条草稿，实际 %d", len(ds))
	}
	if ds[0].Patch["upstream"] != "10.0.0.1:80" {
		t.Errorf("草稿应含 v1 的回源，实际 %v", ds[0].Patch)
	}
	// body_max 必须原样是人写的字符串，不是字节数——渲染是有损的，
	// 这正是快照要存资源模型而不只存渲染产物的原因。
	if ds[0].Patch["body_max"] != "5MB" {
		t.Errorf("请求体上限应原样恢复为 \"5MB\"，实际 %v", ds[0].Patch["body_max"])
	}
	cur, _ := st.GetRoute(ctx, "api.example.com")
	if cur.Upstream != "10.0.0.9:9090" {
		t.Errorf("回滚前线上值不应被改动，实际 %s", cur.Upstream)
	}
}

// 只写回**有差异**的资源。
//
// 把没变的也写成草稿，用户会在确认弹层看到一堆「改动」却全是空 diff，
// 分不清哪些是真要回滚的。
func TestRollbackOnlyWritesChangedResources(t *testing.T) {
	o, st, _ := rollbackRig(t)
	ctx := context.Background()

	for _, d := range []string{"a.example.com", "b.example.com"} {
		_ = st.PutRoute(ctx, model.Route{
			Domain: d, Upstream: "10.0.0.1:80", Block: model.BlockAbort, BodyMax: "1MB",
		})
	}
	v1, err := o.Deploy(ctx, "abiu", nil)
	if err != nil {
		t.Fatal(err)
	}

	// 只改 a
	_ = st.PutRoute(ctx, model.Route{
		Domain: "a.example.com", Upstream: "10.0.0.9:80", Block: model.BlockAbort, BodyMax: "1MB",
	})
	if _, err := o.Deploy(ctx, "abiu", nil); err != nil {
		t.Fatal(err)
	}

	keys, err := o.Rollback(ctx, v1.CfgVersion, "abiu")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "route:a.example.com" {
		t.Fatalf("只有 a 变过，应只写回它，实际 %v", keys)
	}
}

// 回滚到当前基线是空操作，应明确拒绝而不是产生一堆空草稿。
func TestRollbackToCurrentBaselineIsRejected(t *testing.T) {
	o, st, _ := rollbackRig(t)
	ctx := context.Background()
	_ = st.PutRoute(ctx, model.Route{
		Domain: "api.example.com", Upstream: "10.0.0.1:80",
		Block: model.BlockAbort, BodyMax: "1MB",
	})
	v1, err := o.Deploy(ctx, "abiu", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Rollback(ctx, v1.CfgVersion, "abiu"); err == nil {
		t.Fatal("回滚到当前基线应被拒绝")
	}
}

func TestRollbackToUnknownVersionFails(t *testing.T) {
	o, _, _ := rollbackRig(t)
	if _, err := o.Rollback(context.Background(), "cfg-nope", "abiu"); err == nil {
		t.Fatal("回滚到不存在的版本应失败")
	}
}
