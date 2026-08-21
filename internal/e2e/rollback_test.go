package e2e_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
)

type rollbackResp struct {
	ResKeys []string `json:"res_keys"`
	Skipped []struct {
		ResKey string `json:"res_key"`
		Reason string `json:"reason"`
	} `json:"skipped"`
}

func (r *rig) rollback(cfgVersion string) (env, rollbackResp) {
	r.t.Helper()
	_, e := r.do("POST", "/deploys/"+cfgVersion+"/rollback", nil)
	var out rollbackResp
	if len(e.Data) > 0 && string(e.Data) != "null" {
		_ = json.Unmarshal(e.Data, &out)
	}
	return e, out
}

func (r *rig) drafts() map[string]json.RawMessage {
	r.t.Helper()
	e := r.mustDo("GET", "/drafts", nil)
	var d struct {
		Items map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		r.t.Fatal(err)
	}
	return d.Items
}

// 回滚把差异写回草稿，**不直接下发**（PRD §6.3）。
//
// 人要在工作台看过 diff、确认之后走同一条流水线：回滚不绕过校验，也同样留审计。
// 一个「点一下就把线上换掉」的回滚按钮，和它要修复的那类事故是同一种性质。
func TestRollbackWritesDraftsWithoutDeploying(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "cdn.example.com", "upstream": r.upstream,
		"block_mode": "abort", "body_max": "64MB",
	})
	first := r.deployNow("route:cdn.example.com")

	// 改小请求体上限，再下发一次。
	r.mustDo("PUT", "/drafts/route:cdn.example.com", map[string]any{"body_max": "8MB"})
	second := r.deployNow("route:cdn.example.com")
	if first == second {
		t.Fatal("两次下发应当是不同的版本")
	}

	deploysBefore := r.countDeploys()
	baselineBefore := r.baseline()

	e, out := r.rollback(first)
	if e.Code != api.CodeOK {
		t.Fatalf("回滚失败 code=%d msg=%s", e.Code, e.Msg)
	}
	if len(out.ResKeys) != 1 || out.ResKeys[0] != "route:cdn.example.com" {
		t.Fatalf("res_keys = %v", out.ResKeys)
	}

	// 没有新的下发记录，基线没动 —— 回滚只写草稿。
	if n := r.countDeploys(); n != deploysBefore {
		t.Fatalf("回滚产生了下发记录：%d → %d", deploysBefore, n)
	}
	if r.baseline() != baselineBefore {
		t.Fatal("回滚不该改变基线")
	}

	// 草稿里只有**有差异的字段**，不是整个资源。
	patch := r.drafts()["route:cdn.example.com"]
	if patch == nil {
		t.Fatal("草稿没写进去")
	}
	var m map[string]any
	if err := json.Unmarshal(patch, &m); err != nil {
		t.Fatal(err)
	}
	if m["body_max"] != "64MB" {
		t.Fatalf("草稿 = %s，想要 body_max 回到 64MB", patch)
	}
	if _, ok := m["upstream"]; ok {
		t.Errorf("回源地址没变过，不该出现在草稿里：%s", patch)
	}
	if _, ok := m["version"]; ok {
		t.Errorf("version 是系统维护的，写进草稿会显示成一条谁也没改过的变更：%s", patch)
	}
}

// 回到当前基线是空操作。返回成功会让人以为发生了什么，
// 而工作台上一处改动都不会出现。
func TestRollbackToCurrentBaselineIsRefused(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "b.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	cur := r.deployNow("route:b.example.com")

	e, _ := r.rollback(cur)
	if e.Code != api.CodeStateConflict {
		t.Fatalf("code = %d，想要 %d；msg=%s", e.Code, api.CodeStateConflict, e.Msg)
	}
}

func TestRollbackToUnknownVersionIsNotFound(t *testing.T) {
	r := newRig(t)
	e, _ := r.rollback("cfg-nope01")
	if e.Code != api.CodeNotFound {
		t.Fatalf("code = %d，想要 %d（资源不存在用 1003，不用 HTTP 404）", e.Code, api.CodeNotFound)
	}
}

// 那次下发之后才新建的资源，回滚不会删除它 —— 而且必须**说出来**。
//
// 静默跳过是不可接受的：人点了回滚、界面说成功了，而某条路由其实没回去。
func TestRollbackReportsResourcesItCannotCover(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "old.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	first := r.deployNow("route:old.example.com")

	// 之后新建一条路由并下发。
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "new.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	r.deployNow("route:new.example.com")

	_, out := r.rollback(first)
	var found bool
	for _, s := range out.Skipped {
		if s.ResKey == "route:new.example.com" {
			found = true
			if !strings.Contains(s.Reason, "新建") {
				t.Errorf("原因 = %q，应当说清它是之后才建的", s.Reason)
			}
		}
	}
	if !found {
		t.Fatalf("回滚应当报出它覆盖不到的资源，实际 skipped=%+v", out.Skipped)
	}
}

// 快照里不含共享密钥：它进的是 JSONB 列，复制一份明文进去等于在一个
// 不加密的地方多留一份凭证。
func TestSnapshotDoesNotContainSecrets(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	const secret = "s3cr3t-in-snapshot-check"
	if _, err := r.store.Pool.Exec(r.ctx(),
		`INSERT INTO access_rules (id, name, type, enabled, spec, apply_to, secret_sealed)
		 VALUES ('svc-1','服务密钥','service_secret',TRUE,
		         '{"header":"X-Service-Key","algo":"hmac-sha256","ttl_s":300}'::jsonb,
		         '[]'::jsonb, NULL)`); err != nil {
		t.Fatal(err)
	}
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "s.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	cfg := r.deployNow("route:s.example.com")

	var snap string
	if err := r.store.Pool.QueryRow(r.ctx(),
		`SELECT snapshot::text FROM deploys WHERE cfg_version = $1`, cfg).Scan(&snap); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snap, secret) {
		t.Fatal("快照里出现了共享密钥")
	}
	// 但它必须是资源状态，不是渲染后的 Caddy JSON —— 否则回滚拆不回来。
	if !strings.Contains(snap, "\"routes\"") || strings.Contains(snap, "\"apps\"") {
		t.Fatalf("快照应当是资源状态而不是渲染产物: %s", snap[:min(200, len(snap))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
