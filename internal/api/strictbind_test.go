package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
)

// **写错的 key 必须报错，不能静默忽略。**
//
// gin 的 ShouldBindJSON 会丢掉未知字段，于是一个写错的 key 得到 `code: 0`
// ——**请求成功了，而什么也没存进去**。
//
// 前端 agent 就是这么撞上的：他发的是顶层 `dns_credential`，
// 而后端要的是 `dns_provider.credential`。返回 code 0、界面提示
// 「设置已保存」，而 `configured` 一直是 false。
//
// **这是「没生效」那一族里最坏的一种：成功的假象。**
// 报错会让人再试，假象让人走开——他会去查别的地方，
// 因为「保存那一步明明成功了」。
func TestUnknownFieldIsRejectedNotIgnored(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)
	auth := func(req *http.Request) { req.AddCookie(ck) }

	// 前端真实发过的那个形状。
	_, e := do(t, r, "PUT", "/api/v1/settings",
		map[string]any{"dns_credential": "ID,Token"}, auth)
	if e.Code == api.CodeOK {
		t.Fatal("写错的 key 得到了 code 0 —— 那是成功的假象，" +
			"人会以为存进去了然后去查别的地方")
	}
	// 报错里要**点名那个字段**：人多半是把嵌套的 key 写成了顶层，
	// 而他要的信息是「哪个字段不对」，不是「格式错误」。
	if !strings.Contains(e.Msg, "dns_credential") {
		t.Errorf("要点名那个写错的字段：%q", e.Msg)
	}

	// 而正确的形状照常工作，并且真的存进去了。
	_, ok := do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{
			"kind": "dnspod", "domain": "example.com", "sub": "cdn",
			"credential": "ID,Token",
		},
	}, auth)
	if ok.Code != api.CodeOK {
		t.Fatalf("正确的形状不该被拒：code=%d msg=%q", ok.Code, ok.Msg)
	}
	_, got := do(t, r, "GET", "/api/v1/settings", nil, auth)
	if !strings.Contains(string(got.Data), `"configured":true`) {
		t.Errorf("凭证应当真的存进去了：%s", got.Data)
	}
}

// **凭证要有一条删除路径。**
//
// 空串对凭证是「不改动」（凭证不回显，前端带不出原值），所以逐字段清空之后
// 会留下一个**能到达的矛盾状态**：库里有凭证、而没有服务商。
// 前端 agent 在真主控上撞到了它——横幅说「还没配」，凭证徽标同时说「已配置」，
// 两句话都在页面上，而两句都对。
//
// 一份再也用不到、也删不掉的凭证仍然是一把**有效的 API Token**。
func TestClearRemovesProviderAndCredential(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)
	auth := func(req *http.Request) { req.AddCookie(ck) }

	_, e := do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{
			"kind": "dnspod", "domain": "example.com", "sub": "cdn",
			"credential": "ID,Token",
		},
	}, auth)
	if e.Code != api.CodeOK {
		t.Fatalf("先配上：code=%d msg=%q", e.Code, e.Msg)
	}

	// **逐字段清空清不掉凭证** —— 这是那个矛盾状态怎么来的。
	do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{"kind": "", "domain": "", "sub": ""},
	}, auth)
	_, mid := do(t, r, "GET", "/api/v1/settings", nil, auth)
	if !strings.Contains(string(mid.Data), `"configured":true`) {
		t.Fatal("前置条件不成立：逐字段清空之后凭证本该还在（空串 = 不改动）")
	}

	// clear 才真的清掉。
	_, cl := do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{"clear": true},
	}, auth)
	if cl.Code != api.CodeOK {
		t.Fatalf("clear 应当成功：code=%d msg=%q", cl.Code, cl.Msg)
	}
	_, after := do(t, r, "GET", "/api/v1/settings", nil, auth)
	if !strings.Contains(string(after.Data), `"configured":false`) {
		t.Errorf("clear 之后凭证该没了：%s", after.Data)
	}
}

// **clear 与其他字段同时出现时拒绝，不静默取舍。**
//
// `{"clear":true,"kind":"dnspod"}` 有两种合理读法（先清再设 / 清掉一切），
// 而挑一种执行等于替人做了他没做的决定。
func TestClearRejectsBeingMixedWithOtherFields(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)
	auth := func(req *http.Request) { req.AddCookie(ck) }

	_, e := do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{"clear": true, "kind": "dnspod"},
	}, auth)
	if e.Code != api.CodeValidation {
		t.Fatalf("混着给应当被拒，实际 code=%d msg=%q", e.Code, e.Msg)
	}
}
