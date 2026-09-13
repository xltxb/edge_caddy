package api_test

import (
	"net/http"
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
)

// TestPutRuleRefusesToOverwriteWhenAskedNotTo：新建不该静默盖掉别人的规则。
//
// `PUT /rules/:id` 是 upsert——这是它该有的语义，改一条已有规则正是这么走的。
// 问题在**新建**：前端的重名保护是本地那份规则列表（`config.rules.some(...)`），
// 而那份列表可能为空（`fetchAll` 的 /rules 那一支失败过）或陈旧（另一台机器上
// 的人刚建了同名的）。后端不拒，它把已有那条整个换掉，还回 `code: 0`
// ——**静默覆盖别人配好的规则，两边都没有提示**（issue #70）。
//
// 做法是给 PUT 一个「不允许覆盖」的说法，而不是新造一个端点：
// `If-None-Match: *` 是 HTTP 本来就有的语义（RFC 9110 §13.1.2，
// 「只有在目标资源不存在时才执行」），而且不动请求体的形状——
// 加字段要连带改 request-shapes.json 与严格绑定。
//
// **判据是「原规则没被改过」**，不是「回了 1004」——回了错但已经覆盖了，
// 是更坏的一种：人看到报错会以为什么都没发生。
func TestPutRuleRefusesToOverwriteWhenAskedNotTo(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)
	withCookie := func(req *http.Request) { req.AddCookie(ck) }

	body := func(name string) map[string]any {
		return map[string]any{
			"name": name, "type": "ip_whitelist", "enabled": true,
			"apply_to": []string{}, "spec": map[string]any{"ips": []string{"203.0.113.7"}},
		}
	}

	// 先有一条，是别人配好的。
	if _, env := do(t, r, http.MethodPut, "/api/v1/rules/wl", body("别人配的"), withCookie); env.Code != api.CodeOK {
		t.Fatalf("装置：建第一条就失败了 code=%d msg=%s", env.Code, env.Msg)
	}

	// 另一个人「新建」了同名的一条。
	_, env := do(t, r, http.MethodPut, "/api/v1/rules/wl", body("我新建的"), func(req *http.Request) {
		withCookie(req)
		req.Header.Set("If-None-Match", "*")
	})
	if env.Code != api.CodeConflict {
		t.Errorf("重名新建回了 code=%d，想要 %d（1004）", env.Code, api.CodeConflict)
	}

	// **原规则原样留着。**
	_, listEnv := do(t, r, http.MethodGet, "/api/v1/rules", nil, withCookie)
	if !contains(string(listEnv.Data), "别人配的") {
		t.Error("原规则被覆盖了 —— 而它是别人配好的，两边都不会收到提示")
	}
	if contains(string(listEnv.Data), "我新建的") {
		t.Error("被拒的那次写入还是落库了")
	}
}

// 改一条已有规则仍然照常：upsert 是 PUT 该有的语义，这次改动不该动它。
func TestPutRuleStillUpdatesWithoutTheHeader(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)
	withCookie := func(req *http.Request) { req.AddCookie(ck) }

	body := func(name string) map[string]any {
		return map[string]any{
			"name": name, "type": "ip_whitelist", "enabled": true,
			"apply_to": []string{}, "spec": map[string]any{"ips": []string{"203.0.113.7"}},
		}
	}
	do(t, r, http.MethodPut, "/api/v1/rules/wl", body("原名"), withCookie)
	if _, env := do(t, r, http.MethodPut, "/api/v1/rules/wl", body("改过的名"), withCookie); env.Code != api.CodeOK {
		t.Fatalf("不带那个头时应当照常覆盖：code=%d msg=%s", env.Code, env.Msg)
	}
	_, listEnv := do(t, r, http.MethodGet, "/api/v1/rules", nil, withCookie)
	if !contains(string(listEnv.Data), "改过的名") {
		t.Error("改名没生效 —— upsert 是 PUT 该有的语义")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
