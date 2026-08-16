package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("打开数据库: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRouteRoundTrip(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	in := model.Route{
		Domain: "api.example.com", Upstream: "10.8.0.2:8080", Block: model.Block403,
		MTLS: true, Compress: false, BodyMax: "64MB",
		Whitelist: []string{"203.0.113.7", "10.8.0.0/24"}, Version: 7,
	}
	if err := st.PutRoute(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRoute(ctx, in.Domain)
	if err != nil {
		t.Fatal(err)
	}
	if got.Upstream != in.Upstream || got.Block != in.Block || got.BodyMax != in.BodyMax || got.Version != in.Version {
		t.Fatalf("标量字段未往返: %+v", got)
	}
	// 布尔值经 INTEGER 往返最容易出错，且错了会静默改变行为：
	// mtls 丢失意味着回源不再出示客户端证书，源站那边直接拒绝。
	if !got.MTLS || got.Compress {
		t.Fatalf("布尔字段未往返: mtls=%v compress=%v", got.MTLS, got.Compress)
	}
	if len(got.Whitelist) != 2 || got.Whitelist[0] != "203.0.113.7" {
		t.Fatalf("白名单未往返: %v", got.Whitelist)
	}
}

// 空白名单必须往返成空切片而不是 nil：渲染器对两者行为一致，
// 但 JSON 序列化成 null 会让前端拿到 null 而非 []，表单渲染就崩了。
func TestEmptyWhitelistRoundTripsAsSlice(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	_ = st.PutRoute(ctx, model.Route{Domain: "a.example.com", Upstream: "x:1", Block: model.BlockAbort, BodyMax: "1MB"})
	got, _ := st.GetRoute(ctx, "a.example.com")
	if got.Whitelist == nil {
		t.Fatal("空白名单应为空切片而非 nil")
	}
}

func TestDeleteMissingRouteIsNotFound(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	if err := st.DeleteRoute(ctx, "nope.example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("删除不存在的路由应返回 ErrNotFound，实际 %v", err)
	}
	if _, err := st.GetRoute(ctx, "nope.example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("查询不存在的路由应返回 ErrNotFound，实际 %v", err)
	}
}

// 空 patch 等于「这条资源已经没有待下发改动」，应删除该行而不是存一个空对象。
// 存空对象会让工作台把它当成一处待推送变更，出现推不掉的幽灵改动。
func TestEmptyPatchRemovesDraft(t *testing.T) {
	st, ctx, now := newStore(t), context.Background(), time.Now()
	key := "route:api.example.com"
	if err := st.PutDraft(ctx, key, map[string]any{"upstream": "10.0.0.9:80"}, "abiu", now); err != nil {
		t.Fatal(err)
	}
	if ds, _ := st.ListDrafts(ctx); len(ds) != 1 {
		t.Fatalf("应有 1 条草稿，实际 %d", len(ds))
	}
	if err := st.PutDraft(ctx, key, map[string]any{}, "abiu", now); err != nil {
		t.Fatal(err)
	}
	if ds, _ := st.ListDrafts(ctx); len(ds) != 0 {
		t.Fatalf("空 patch 应删除草稿，实际还剩 %d 条", len(ds))
	}
}

// 下发只清**本次勾选**的草稿。这条守的是「推送时勾选」这个决定：
// 若下发顺手清空全部草稿，别人还没推的改动就被无声吞掉了。
func TestDeleteDraftsOnlyRemovesSelected(t *testing.T) {
	st, ctx, now := newStore(t), context.Background(), time.Now()
	_ = st.PutDraft(ctx, "route:a.example.com", map[string]any{"upstream": "1"}, "abiu", now)
	_ = st.PutDraft(ctx, "route:b.example.com", map[string]any{"upstream": "2"}, "ops-bot", now)

	if err := st.DeleteDrafts(ctx, []string{"route:a.example.com"}); err != nil {
		t.Fatal(err)
	}
	left, _ := st.ListDrafts(ctx)
	if len(left) != 1 || left[0].ResKey != "route:b.example.com" {
		t.Fatalf("未勾选的草稿必须留着，实际剩余 %+v", left)
	}
	if left[0].UpdatedBy != "ops-bot" {
		t.Errorf("草稿作者应保留（确认弹层要逐条标注），实际 %q", left[0].UpdatedBy)
	}
}

// 成功/失败计数由结果表现算，重复写入同一节点的结果不应让计数漂。
func TestDeployCountsAreRecomputedNotAccumulated(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	id, err := st.CreateDeploy(ctx, model.Deploy{
		CfgVersion: "cfg-aaa111", Operator: "abiu",
		ResKeys:   []string{"route:api.example.com"},
		Snapshot:  model.Snapshot{Rendered: map[string]any{"http": map[string]any{}}},
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	put := func(node, state, detail string) {
		if err := st.PutDeployResult(ctx, model.DeployResult{
			DeployID: id, NodeID: node, State: state, Detail: detail}); err != nil {
			t.Fatal(err)
		}
	}
	put("node-hk-01", "ok", "31ms")
	put("node-us-01", "fail", "deadline exceeded")
	// 重推该节点后再写一次结果：计数必须是 2 成功 0 失败，而不是累加成 3 条
	put("node-us-01", "ok", "44ms")

	ds, results, err := st.ListDeploys(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 {
		t.Fatalf("应有 1 条下发记录，实际 %d", len(ds))
	}
	if ds[0].OKCount != 2 || ds[0].FailCount != 0 {
		t.Fatalf("重推后计数应为 ok=2 fail=0，实际 ok=%d fail=%d", ds[0].OKCount, ds[0].FailCount)
	}
	if len(results[id]) != 2 {
		t.Fatalf("同一节点的结果应被覆盖而非追加，实际 %d 条", len(results[id]))
	}
}

func TestBaselineIsLatestDeploy(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	if b, err := st.Baseline(ctx); err != nil || b != "" {
		t.Fatalf("尚无下发时基线应为空串，实际 %q err=%v", b, err)
	}
	for _, v := range []string{"cfg-111111", "cfg-222222"} {
		if _, err := st.CreateDeploy(ctx, model.Deploy{
			CfgVersion: v, Operator: "abiu", CreatedAt: time.Now(),
			Snapshot: model.Snapshot{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := st.Baseline(ctx)
	if err != nil || b != "cfg-222222" {
		t.Fatalf("基线应是最近一次下发，实际 %q err=%v", b, err)
	}
}

// 非法处置方式必须被 CHECK 约束挡下。把校验只放在应用层，
// 意味着任何绕过应用层的写入都能让库里出现渲染器无法处理的值。
func TestSchemaRejectsUnknownBlockMode(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	_, err := st.DB().ExecContext(ctx,
		`INSERT INTO proxy_routes (domain, upstream, block_mode) VALUES ('x.example.com','h:1','reject')`)
	if err == nil {
		t.Fatal("CHECK 约束应拒绝未知的 block_mode")
	}
}
