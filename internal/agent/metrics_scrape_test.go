package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/caddytest"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
)

// TestRequestTotalsCountEachRequestOnce：req_total 与 origin_total 的口径。
//
// 契约 §3：「origin_rate 是回源率百分比：**到达 upstream 的请求 ÷ 边缘收到的
// 总请求**」。原先的实现把 caddy_http_requests_total 的**所有**行求和，而那个
// 计数器是按 handler 各记一次的——本机 caddy 2.11.4 实测，两个请求得到：
//
//	caddy_http_requests_total{handler="headers",server="edge"}       2
//	caddy_http_requests_total{handler="reverse_proxy",server="edge"} 2
//
// 相加是 4，真实值是 2：**回源率被腰斩**（issue #41）。
//
// 分子也有一半：在 forward_auth 处被拒的请求同样记在 handler="reverse_proxy"
// 上（forward_auth 渲染出来就是一个 reverse_proxy），而它一个字节都没到源站。
//
// **判据是「我发了几个请求」**，不是「指标之间对不对得上」——后者两个数一起
// 错的时候仍然自洽。
func TestRequestTotalsCountEachRequestOnce(t *testing.T) {
	const secret = "s3cr3t-shared-key"
	up := echoUpstream(t)
	c := caddytest.New(t)

	rule := model.Rule{
		ID: "svc-1", Type: model.RuleServiceSecret, Enabled: true,
		ApplyTo: []string{"prot.example.com"}, Secret: secret,
		Spec: model.RuleSpec{Header: "X-Service-Key", TTLSeconds: 300},
	}
	v := serveVerifyServer(t, c, render.VerifyRules([]model.Rule{rule}))

	// **策略开着**：那会在路由前面挂一个 headers handler，而它正是让
	// 「把所有行相加」翻倍的那一个。关着的话这条测试看不见那个 bug。
	pol := render.Policies{}
	pol.Log.StripHeaders = true
	cfg, issues := render.Render(
		[]model.Route{
			{Domain: "plain.example.com", Upstream: up, BlockMode: model.BlockAbort},
			{Domain: "prot.example.com", Upstream: up, BlockMode: model.BlockAbort},
		},
		[]model.Rule{rule}, nil, pol,
		render.Options{HTTPListen: c.EdgeListen(), VerifyAddr: c.VerifyDial(), LogDir: c.LogDir()})
	if len(issues) > 0 {
		t.Fatalf("渲染报了问题: %v", issues)
	}
	caddy := agent.NewCaddyClient(c.AdminURL())
	if _, err := caddy.ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("Caddy 拒绝了配置: %v", err)
	}

	// 三个请求，其中两个真的到源站，一个在 forward_auth 处被拒。
	if code, _ := c.Get("plain.example.com", "/", nil); code != 200 {
		t.Fatalf("普通域名应当 200，实际 %d", code)
	}
	sig := signHMAC(secret, "GET", "/", time.Now())
	if code, _ := c.Get("prot.example.com", "/", map[string]string{"X-Service-Key": sig}); code != 200 {
		t.Fatalf("验签通过的请求应当 200，实际 %d", code)
	}
	if code, _ := c.Get("prot.example.com", "/", map[string]string{"X-Service-Key": "bogus"}); code != 403 {
		t.Fatalf("验签失败的请求应当 403，实际 %d", code)
	}
	time.Sleep(300 * time.Millisecond)

	req, origin := agent.ScrapeTotals(context.Background(), caddy, v)
	if req != 3 {
		t.Errorf("边缘收到 3 个请求，req_total 报的是 %d —— "+
			"caddy_http_requests_total 是按 handler 各记一次的，相加就会翻倍", req)
	}
	if origin != 2 {
		t.Errorf("只有 2 个请求到了源站，origin_total 报的是 %d —— "+
			"在 forward_auth 处被拒的那个也记在 handler=\"reverse_proxy\" 上", origin)
	}
}
