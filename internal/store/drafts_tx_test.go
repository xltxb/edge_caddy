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

// TestPutDraftRejectsNonObjectPatch：草稿必须是一个对象，写入时就拒。
//
// PutDraft 原先这样判空：
//
//	var m map[string]any
//	if err := json.Unmarshal(patch, &m); err == nil && len(m) == 0 { 删除 }
//
// **err 被丢掉了**，于是 `[1,2]` / `"x"` / `123` 这类合法 JSON 但不是对象的东西
// 照样入库——jsonb 列只要求合法 JSON，不要求是对象（issue #58）。
//
// 它的代价不在这一步：之后 deploy 的 mergeInto 对它 Unmarshal 进 map 会失败，
// Deploy 与 Preview 双双 500，**而人在界面上找不到入口删它**。一个从界面上
// 解不开的死局。
//
// 草稿按 CONTEXT.md 的定义就是 Partial（对象），这个约束此前没有任何一处守。
func TestPutDraftRejectsNonObjectPatch(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()

	for _, bad := range []string{`[1,2]`, `"x"`, `123`, `true`, `null`, `{`} {
		if err := st.PutDraft(ctx, "route:a.example.com", json.RawMessage(bad), "tester"); err == nil {
			t.Errorf("patch=%s 应当被拒，而它存进去了", bad)
		}
	}

	drafts, err := st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 0 {
		t.Errorf("一条都不该写进去，实际有 %d 条", len(drafts))
	}
}

// TestPutDraftStillTreatsEmptyObjectAsDelete：空对象仍然等于删除。
//
// 那条捷径是有意的（把最后一处改动撤回 = 这条草稿不存在了），
// 而上面那道新闸不该把它带坏。
func TestPutDraftStillTreatsEmptyObjectAsDelete(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	const key = "route:a.example.com"

	if err := st.PutDraft(ctx, key, json.RawMessage(`{"upstream":"127.0.0.1:1"}`), "tester"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDraft(ctx, key, json.RawMessage(`{}`), "tester"); err != nil {
		t.Fatalf("空对象是撤回，不是错误: %v", err)
	}
	drafts, err := st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 0 {
		t.Errorf("空对象应当把这条草稿删掉，实际还剩 %d 条", len(drafts))
	}
}

// TestPutDraftsRejectsNonObjects：整批写入也要过 #58 那道闸。
//
// PutDraft 在入口处拒非对象，理由是那些东西入库之后 deploy 的 mergeInto 会
// 失败，Deploy 与 Preview 双双 500，**而人在界面上找不到入口删它**。
//
// PutDrafts 是同一张表的另一个入口，而它直接走 putDraft。`{` 那样的非法 JSON
// 会被 PostgreSQL 的 jsonb 列拦下（TestPutDraftsIsAllOrNothing 靠的就是它），
// 但 `[1,2]` / `"x"` / `123` / `null` 都是合法 JSON —— **数据库那一关放行，
// 而它们正是 #58 要挡的那一批**。
//
// 回滚是 PutDrafts 唯一的调用方，它写的是快照与 live 的差异。差异算错一次，
// 留下的就是一个从界面上解不开的死局。
func TestPutDraftsRejectsNonObjects(t *testing.T) {
	for _, bad := range []string{`[1,2]`, `"x"`, `123`, `null`} {
		t.Run(bad, func(t *testing.T) {
			st := testdb.New(t)
			ctx := context.Background()

			err := st.PutDrafts(ctx, map[string]json.RawMessage{
				"route:a.example.com": json.RawMessage(`{"upstream":"127.0.0.1:8080"}`),
				"route:b.example.com": json.RawMessage(bad),
			}, "tester")
			if err == nil {
				t.Fatalf("%s 不是一个对象，整批应当被拒", bad)
			}

			// **整批被拒，就一条都不该留下。** 只断言报错的话，一个「先写好的、
			// 撞到坏的才回滚」的实现与一个「入口就拦」的实现看起来一样。
			drafts, err := st.ListDrafts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(drafts) != 0 {
				t.Fatalf("这一批被拒了，表里却留下 %d 条", len(drafts))
			}
		})
	}
}
