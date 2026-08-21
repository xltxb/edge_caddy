package e2e_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
)

type previewResp struct {
	Before   string `json:"before"`
	After    string `json:"after"`
	Baseline string `json:"baseline"`
	Targets  []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"targets"`
	Validation struct {
		OK     bool `json:"ok"`
		Errors []struct {
			ResKey string `json:"res_key"`
			Field  string `json:"field"`
			Reason string `json:"reason"`
		} `json:"errors"`
	} `json:"validation"`
}

func (r *rig) preview(resKeys ...string) (env, previewResp) {
	r.t.Helper()
	_, e := r.do("POST", "/deploys/preview", map[string]any{"res_keys": resKeys})
	var p previewResp
	if len(e.Data) > 0 && string(e.Data) != "null" {
		if err := json.Unmarshal(e.Data, &p); err != nil {
			r.t.Fatalf("预览响应解析失败: %s", e.Data)
		}
	}
	return e, p
}

// before / after 都是后端渲染的字节全文。权威性来自「两份都是后端渲染的」，
// 不来自谁算的 diff —— 所以 diff 归前端，全站只有一套 LCS 实现。
func TestPreviewReturnsTwoBackendRenderings(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "p.example.com", "upstream": "127.0.0.1:1111", "block_mode": "abort",
	})
	r.mustDo("PUT", "/drafts/route:p.example.com", map[string]any{"upstream": "127.0.0.1:2222"})

	e, p := r.preview("route:p.example.com")
	if e.Code != api.CodeOK {
		t.Fatalf("code = %d msg=%s", e.Code, e.Msg)
	}
	if !p.Validation.OK {
		t.Fatalf("这组输入应当校验通过: %+v", p.Validation.Errors)
	}
	if p.Before == "" || p.After == "" {
		t.Fatal("before 与 after 都必须是渲染全文")
	}
	if p.Before == p.After {
		t.Fatal("草稿改了回源地址，两份渲染不该相同")
	}
	if !strings.Contains(p.Before, "127.0.0.1:1111") {
		t.Error("before 应当是当前基线的内容")
	}
	if !strings.Contains(p.After, "127.0.0.1:2222") {
		t.Error("after 应当已经叠加了草稿")
	}
}

// 校验没过时 **code 仍然是 0**。预览成功地告诉了你「校验没过」，
// 那不是请求失败；只有 POST /deploys 才用 1002 拒绝执行。
func TestPreviewValidationFailureIsNotARequestFailure(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "q.example.com", "upstream": "127.0.0.1:1", "block_mode": "abort",
	})
	r.mustDo("PUT", "/drafts/route:q.example.com", map[string]any{"upstream": "没有端口"})

	e, p := r.preview("route:q.example.com")
	if e.Code != api.CodeOK {
		t.Fatalf("预览的 code = %d，想要 0 —— 校验没过不等于请求失败", e.Code)
	}
	if p.Validation.OK {
		t.Fatal("这组输入应当校验失败")
	}
	if len(p.Validation.Errors) == 0 || p.Validation.Errors[0].Field != "upstream" {
		t.Fatalf("errors = %+v，想要定位到 upstream 字段", p.Validation.Errors)
	}
	if p.After != "" {
		t.Error("校验没过时不该给出 after —— 那份配置不存在")
	}
}

// 预览是 dry-run：不要求有在线节点，也不产生任何下发记录。
func TestPreviewIsPureDryRun(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "dry.example.com", "upstream": r.upstream, "block_mode": "abort",
	})

	e, p := r.preview("route:dry.example.com")
	if e.Code != api.CodeOK {
		t.Fatalf("没有在线节点时预览也应当成功，code=%d msg=%s", e.Code, e.Msg)
	}
	if p.Targets == nil {
		t.Error("targets 应当是空数组而不是 null")
	}
	if len(p.Targets) != 0 {
		t.Errorf("没有节点接入，targets 应当为空，实际 %+v", p.Targets)
	}
	if n := r.countDeploys(); n != 0 {
		t.Fatalf("预览不该产生下发记录，实际有 %d 条", n)
	}
	// 草稿也不该被动过 —— 预览什么都不改。
	drafts := r.mustDo("GET", "/drafts", nil)
	if !strings.Contains(string(drafts.Data), "items") {
		t.Fatal("草稿端点响应异常")
	}
}

// 私钥不进浏览器：before / after 都不含 apps/tls（ADR-0007 的补充）。
func TestPreviewExcludesTLSApp(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "tls.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	_, p := r.preview("route:tls.example.com")
	for name, body := range map[string]string{"before": p.Before, "after": p.After} {
		if strings.Contains(body, `"tls"`) || strings.Contains(body, "load_pem") {
			t.Errorf("%s 里出现了 apps/tls —— 内联证书的私钥不该进浏览器", name)
		}
	}
}

// 预览**不返回 cfg_version**。新版本号是在 POST /deploys 那一刻生成的，
// 预览时给一个只会与实际下发不符的号码，正是我们一直在拦的那类
// 「界面给出兑现不了的承诺」。
func TestPreviewDoesNotFabricateAVersionNumber(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "v.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	e, _ := r.preview("route:v.example.com")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(e.Data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["cfg_version"]; ok {
		t.Fatal("预览不该返回 cfg_version —— 那个号码在下发时才生成，提前给出必然与实际不符")
	}
	if _, ok := raw["baseline"]; !ok {
		t.Fatal("预览应当返回 baseline，弹层据此说「基线 X → 新版本（下发时生成）」")
	}
}

// 预览里出现的节点就是下发会广播到的那些。
func TestPreviewTargetsMatchOnlineNodes(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "t.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	_, p := r.preview("route:t.example.com")
	if len(p.Targets) != 1 || p.Targets[0].ID != "node-hk-01" {
		t.Fatalf("targets = %+v，想要 [node-hk-01]", p.Targets)
	}
}
