package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
)

// TestPutDraftsIsAllOrNothing：一批草稿要么全写进去，要么一条都不写。
//
// 回滚原先是逐条 PutDraft，中途失败就地返回，而 out.ResKeys 还是 nil——
// 于是工作台里亮着前几条、接口回 500、响应里一个 res_key 都不报。人重试或
// 放弃之后按「待下发」发出去的，是**半个回滚**（issue #44）。
//
// snapshot.go:29 写着「静默跳过是不可接受的」。半个回滚比静默跳过更糟：
// 它连自己做到哪一步了都不说。
//
// **故障是真的，不是注入的**：patch 列是 jsonb，一段不合法的 JSON 会被
// PostgreSQL 当场拒绝。这条路径在生产里也到得了——#58 记着 PutDraft 会把
// 解析错误吞掉，于是什么形状的东西都可能走到这一步。
func TestPutDraftsIsAllOrNothing(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()

	// 先放一条已经存在的草稿：这一批失败之后，它也不该被动过。
	const existing = "route:old.example.com"
	if err := st.PutDraft(ctx, existing,
		json.RawMessage(`{"upstream":"127.0.0.1:1"}`), "tester"); err != nil {
		t.Fatal(err)
	}

	err := st.PutDrafts(ctx, map[string]json.RawMessage{
		"route:a.example.com": json.RawMessage(`{"upstream":"127.0.0.1:8080"}`),
		"route:b.example.com": json.RawMessage(`{"upstream":"127.0.0.1:8081"}`),
		// 这一条 PostgreSQL 会拒绝。
		"route:c.example.com": json.RawMessage(`{`),
		existing:              json.RawMessage(`{"upstream":"127.0.0.1:9999"}`),
	}, "tester")
	if err == nil {
		t.Fatal("一条非法 patch 应当让整批失败")
	}

	drafts, err := st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range drafts {
		got[d.ResKey] = string(d.Patch)
	}

	for _, k := range []string{"route:a.example.com", "route:b.example.com", "route:c.example.com"} {
		if _, ok := got[k]; ok {
			t.Errorf("整批失败了，%s 却写进去了——工作台里会亮着半个回滚", k)
		}
	}
	// 已有的那条要保持原样：一次失败的回滚不该顺手改掉别的东西。
	if got[existing] != `{"upstream": "127.0.0.1:1"}` && got[existing] != `{"upstream":"127.0.0.1:1"}` {
		t.Errorf("已有草稿被这次失败的批次改成了 %q", got[existing])
	}
}

// TestPutDraftsWritesThemAll：正常情形下整批都写进去。
//
// 少了这条，一个「永远返回错误、什么也不写」的实现同样能让上面那条绿。
func TestPutDraftsWritesThemAll(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()

	want := map[string]json.RawMessage{
		"route:a.example.com": json.RawMessage(`{"upstream":"127.0.0.1:8080"}`),
		"rule:svc-1":          json.RawMessage(`{"enabled":true}`),
	}
	if err := st.PutDrafts(ctx, want, "tester"); err != nil {
		t.Fatal(err)
	}

	drafts, err := st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, d := range drafts {
		seen[d.ResKey] = true
		if d.UpdatedBy != "tester" {
			t.Errorf("%s 的 updated_by 是 %q", d.ResKey, d.UpdatedBy)
		}
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("%s 没有写进去", k)
		}
	}
}

var _ = store.ErrNotFound
