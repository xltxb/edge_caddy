package agent_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/caddytest"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
)

// upstream 起一个本地源站，返回它的 host:port。
func upstream(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// 这条是本切片验收标准的核心：渲染 → 应用到真 Caddy → 一个真请求被代理到上游。
//
// 它同时把 ADR-0004 承认的盲区兜住了——golden 快照只能证明「渲染出了我们想要的」，
// 证明不了 Caddy 会接受它。
func TestRenderedConfigIsAcceptedAndActuallyProxies(t *testing.T) {
	up := upstream(t, "UPSTREAM OK")
	c := caddytest.New(t)

	cfg, issues := render.Render([]model.Route{{
		Domain: "api.example.com", Upstream: up,
		BlockMode: model.BlockAbort, Compress: true, BodyMax: "5MB",
	}}, nil, nil, render.Policies{}, render.Options{HTTPListen: c.EdgeListen()})
	if len(issues) > 0 {
		t.Fatalf("渲染报了校验问题: %v", issues)
	}

	took, err := agent.NewCaddyClient(c.AdminURL()).ApplyConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Caddy 拒绝了渲染产出: %v", err)
	}
	if took <= 0 {
		t.Error("应用耗时应当为正——控制台上那个「31ms」来自这里")
	}

	code, body := c.Get("api.example.com", "/", nil)
	if code != 200 || body != "UPSTREAM OK" {
		t.Fatalf("经 Caddy 回源得到 %d %q，想要 200 \"UPSTREAM OK\"", code, body)
	}
}

// ADR-0010：一台刚装完官方包、Caddyfile 为空的机器，运行配置里没有 apps 键，
// 直接 POST 单个 app 会 500。这条锁住那个事实本身。
func TestPostingAppWithoutAppsKeyIs500(t *testing.T) {
	c := caddytest.New(t)
	status, body := c.PostApp("http", []byte(`{"servers":{}}`))
	if status != 500 {
		t.Fatalf("没有 apps 键时 POST /config/apps/http 得到 %d，想要 500", status)
	}
	if !strings.Contains(body, "invalid traversal path") {
		t.Errorf("报错原文 = %q，想要含 invalid traversal path", body)
	}
}

// 而 Agent 必须能在那种机器上成功——它会先用 PUT 补一个空 apps 对象。
// 用 PUT 而不是 POST：POST 到已存在的键会替换它，把别的 app 抹掉。
func TestApplySucceedsOnFreshCaddyWithoutAppsKey(t *testing.T) {
	up := upstream(t, "FRESH BOX")
	c := caddytest.New(t)

	if _, ok := c.Config()["apps"]; ok {
		t.Fatal("fixture 应当刻意不带 apps 键——盖住这个情形正是上一版踩的坑")
	}

	cfg, _ := render.Render([]model.Route{{
		Domain: "a.example.com", Upstream: up, BlockMode: model.BlockAbort,
	}}, nil, nil, render.Policies{}, render.Options{HTTPListen: c.EdgeListen()})

	if _, err := agent.NewCaddyClient(c.AdminURL()).ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("在没有 apps 键的机器上应用失败: %v", err)
	}
	if code, body := c.Get("a.example.com", "/", nil); code != 200 || body != "FRESH BOX" {
		t.Fatalf("得到 %d %q", code, body)
	}
}

// 一份坏配置被拒绝，且**在跑的配置存活**。
// 「一份坏配置打不挂节点」是 ADR-0004 决定不做主控侧预校验的全部依据。
func TestBadConfigIsRejectedAndRunningConfigSurvives(t *testing.T) {
	up := upstream(t, "STILL ALIVE")
	c := caddytest.New(t)
	cli := agent.NewCaddyClient(c.AdminURL())
	ctx := context.Background()

	good, _ := render.Render([]model.Route{{
		Domain: "live.example.com", Upstream: up, BlockMode: model.BlockAbort,
	}}, nil, nil, render.Policies{}, render.Options{HTTPListen: c.EdgeListen()})
	if _, err := cli.ApplyConfig(ctx, good); err != nil {
		t.Fatalf("基线配置应当被接受: %v", err)
	}

	bad := []byte(`{"apps":{"http":{"servers":{"edge":{"listen":[":1"],"routes":[{"handle":[{"handler":"no_such_handler"}]}]}}}}}`)
	_, err := cli.ApplyConfig(ctx, bad)
	if err == nil {
		t.Fatal("未知 handler 的配置应当被拒绝")
	}

	var rejected *agent.RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("应当报成 RejectedError（节点回应了但 Caddy 拒绝），实际 %T: %v", err, err)
	}

	if code, body := c.Get("live.example.com", "/", nil); code != 200 || body != "STILL ALIVE" {
		t.Fatalf("坏配置之后在跑的配置没存活：得到 %d %q", code, body)
	}
}

// 连不上 Admin 与「Caddy 拒绝了配置」必须是两种不同的错误。
// 这是 ADR-0005 分类重试的唯一依据：前者重试，后者不重试。
func TestUnreachableAdminIsNotARejection(t *testing.T) {
	cli := agent.NewCaddyClient("http://127.0.0.1:1") // 没人监听
	_, err := cli.ApplyConfig(context.Background(),
		[]byte(`{"apps":{"http":{"servers":{}}}}`))
	if err == nil {
		t.Fatal("连不上 Admin 应当报错")
	}
	var rejected *agent.RejectedError
	if errors.As(err, &rejected) {
		t.Fatal("连不上 Admin 被归成了 RejectedError——那会让传输层故障不再重试")
	}
}

// 处置方式：白名单之外的流量按 abort 静默断连，不暴露服务是否存在。
func TestWhitelistDeniesWithAbort(t *testing.T) {
	up := upstream(t, "SECRET")
	c := caddytest.New(t)

	cfg, issues := render.Render([]model.Route{{
		Domain: "wl.example.com", Upstream: up, BlockMode: model.BlockAbort,
		Whitelist: []string{"203.0.113.7"}, // 不含 127.0.0.1
	}}, nil, nil, render.Policies{}, render.Options{HTTPListen: c.EdgeListen()})
	if len(issues) > 0 {
		t.Fatalf("渲染报了问题: %v", issues)
	}
	if _, err := agent.NewCaddyClient(c.AdminURL()).ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("Caddy 拒绝了带白名单的配置: %v", err)
	}

	code, body := c.Get("wl.example.com", "/", nil)
	if code == 200 {
		t.Fatalf("白名单之外的来源不该拿到响应，却得到 200 %q", body)
	}
	if strings.Contains(body, "SECRET") {
		t.Fatal("上游内容泄漏给了白名单之外的来源")
	}
}

// 403 与 abort 的区别是可观察的：403 会明确告诉对方「这里有东西但你不能进」。
func TestWhitelistDenyWith403IsDistinguishableFromAbort(t *testing.T) {
	up := upstream(t, "SECRET")
	c := caddytest.New(t)

	cfg, _ := render.Render([]model.Route{{
		Domain: "wl403.example.com", Upstream: up, BlockMode: model.Block403,
		Whitelist: []string{"203.0.113.7"},
	}}, nil, nil, render.Policies{}, render.Options{HTTPListen: c.EdgeListen()})
	if _, err := agent.NewCaddyClient(c.AdminURL()).ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if code, _ := c.Get("wl403.example.com", "/", nil); code != 403 {
		t.Fatalf("处置方式为 403 时应当得到 403，实际 %d", code)
	}
}
