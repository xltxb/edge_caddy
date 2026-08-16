package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/auth"
	"github.com/xltxb/edge_caddy/internal/deploy"
	"github.com/xltxb/edge_caddy/internal/enroll"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

const testPassword = "correct horse battery staple"

type rig struct {
	h   http.Handler
	st  *store.Store
	au  *auth.Manager
	tun *emptyTunnel
}

// newRig 起一个真实路由。withPassword=false 时不设口令，用来覆盖「鉴权未启用」那条路径。
func newRig(t *testing.T, withPassword bool) *rig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	au := auth.New(st)
	if withPassword {
		if err := au.SetPassword(context.Background(), testPassword); err != nil {
			t.Fatal(err)
		}
	}
	// 用真的编排器配一个没有节点在线的隧道：「没有在线节点」正是要验的行为之一，
	// 把编排器整个 mock 掉就测不到它了。
	tun := &emptyTunnel{}
	orch := deploy.New(st, tun, nil)
	h := api.New(api.Deps{Store: st, Auth: au, Enroll: enroll.New(st), Deploy: orch, Nodes: tun})
	return &rig{h: h, st: st, au: au, tun: tun}
}

// emptyTunnel 模拟「一个节点都没连上」。
// emptyTunnel 默认「一个节点都没连上」；nodes 非空时假装那些节点在线。
type emptyTunnel struct {
	nodes    []string
	report   tunnel.ProbeReport
	probeErr error
}

func (e *emptyTunnel) Connected() []string                  { return e.nodes }
func (e *emptyTunnel) Send(string, *edgev1.MasterMsg) error { return nil }
func (e *emptyTunnel) Probe(context.Context, string, time.Duration) (tunnel.ProbeReport, error) {
	return e.report, e.probeErr
}

func (r *rig) do(t *testing.T, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		blob, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(blob)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

// login 走真实登录接口拿会话 Cookie。
func (r *rig) login(t *testing.T) *http.Cookie {
	t.Helper()
	rec := r.do(t, http.MethodPost, "/api/v1/login",
		map[string]string{"user": auth.AdminUser, "password": testPassword}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatal("登录响应里没有会话 Cookie")
	return nil
}

func envelope(t *testing.T, rec *httptest.ResponseRecorder) (int, map[string]any) {
	t.Helper()
	var out struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
		Msg  string         `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v（%s）", err, rec.Body.String())
	}
	return out.Code, out.Data
}

// 未登录访问受保护接口必须 401。
func TestProtectedRoutesRequireSession(t *testing.T) {
	r := newRig(t, true)
	for _, path := range []string{"/api/v1/nodes", "/api/v1/me"} {
		rec := r.do(t, http.MethodGet, path, nil, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s 未登录应返回 401，实际 %d", path, rec.Code)
		}
	}
	rec := r.do(t, http.MethodPost, "/api/v1/nodes/token", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("签发接入 Token 未登录应返回 401，实际 %d", rec.Code)
	}
}

// 会话 Cookie 必须是 HttpOnly + SameSite=Lax。
//
// HttpOnly 让 JS 读不到它，XSS 也偷不走；SameSite=Lax 让跨站请求不带上它，
// 挡掉 CSRF 的常见形态。这两个属性一旦被摘掉，界面看起来完全正常，
// 所以只能靠断言守住。
func TestSessionCookieIsHardened(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	if !c.HttpOnly {
		t.Error("会话 Cookie 必须是 HttpOnly")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("会话 Cookie 应为 SameSite=Lax，实际 %v", c.SameSite)
	}
	if c.Value == "" {
		t.Error("会话 Cookie 不应为空")
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	r := newRig(t, true)
	rec := r.do(t, http.MethodPost, "/api/v1/login",
		map[string]string{"user": auth.AdminUser, "password": "wrong"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错口令应返回 401，实际 %d", rec.Code)
	}
	code, _ := envelope(t, rec)
	if code == 0 {
		t.Error("失败响应的 code 不应为 0")
	}
	if !strings.Contains(rec.Body.String(), "不正确") {
		t.Errorf("失败响应应带用户可读的中文说明: %s", rec.Body.String())
	}
}

func TestLoginThenAccessThenLogout(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)

	rec := r.do(t, http.MethodGet, "/api/v1/me", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录后访问应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_, data := envelope(t, rec)
	if data["user"] != auth.AdminUser {
		t.Errorf("/me 应返回当前用户，实际 %v", data)
	}

	if rec := r.do(t, http.MethodPost, "/api/v1/logout", nil, c); rec.Code != http.StatusOK {
		t.Fatalf("登出应成功，实际 %d", rec.Code)
	}
	if rec := r.do(t, http.MethodGet, "/api/v1/me", nil, c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("登出后旧 Cookie 应失效，实际 %d", rec.Code)
	}
}

// 尚未设置口令时接口敞开，且 /me 要如实说明鉴权是关的。
//
// 这条固化的是一个危险的默认值（设计稿登录页写明的首次部署行为）。前端靠
// auth_required 决定要不要跳登录页——它要是撒谎，用户会以为自己受保护着。
func TestOpenAccessWhenNoPasswordSet(t *testing.T) {
	r := newRig(t, false)
	rec := r.do(t, http.MethodGet, "/api/v1/nodes", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("未设置口令时接口应敞开，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_, data := envelope(t, r.do(t, http.MethodGet, "/api/v1/me", nil, nil))
	if data["auth_required"] != false {
		t.Errorf("/me 应如实报告鉴权未启用，实际 %v", data)
	}
}

// 节点列表反映心跳写入的真实数据。
func TestNodesListReflectsHeartbeats(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	if err := r.st.UpsertNodeSeen(ctx, "node-hk-01", "cfg-2f9a1c", nowUTC()); err != nil {
		t.Fatal(err)
	}

	_, data := envelope(t, r.do(t, http.MethodGet, "/api/v1/nodes", nil, r.login(t)))
	nodes, ok := data["nodes"].([]any)
	if !ok || len(nodes) != 1 {
		t.Fatalf("应返回 1 个节点，实际 %v", data)
	}
	n := nodes[0].(map[string]any)
	if n["id"] != "node-hk-01" {
		t.Errorf("节点 ID 不对: %v", n)
	}
	if n["cfg"] != "cfg-2f9a1c" {
		t.Errorf("节点应带上其配置版本（漂移判定要用）: %v", n)
	}
}

// 签发的接入 Token 必须真的可用——这是「装饰性凭据」最容易发生的地方：
// 接口返回一个漂亮的串，但它压根没被登记，于是拿去接入必然失败。
func TestIssuedTokenIsActuallyUsable(t *testing.T) {
	r := newRig(t, true)
	_, data := envelope(t, r.do(t, http.MethodPost, "/api/v1/nodes/token", nil, r.login(t)))

	tok, _ := data["token"].(string)
	if tok == "" {
		t.Fatal("应返回接入 Token")
	}
	if data["install"] == nil || !strings.Contains(data["install"].(string), tok) {
		t.Errorf("应返回含该 Token 的安装命令，实际 %v", data["install"])
	}
	// 真拿去消费一次：接口签发的 Token 必须已经登记在册
	if err := enroll.New(r.st).Consume(context.Background(), tok, "node-hk-01"); err != nil {
		t.Fatalf("接口签发的 Token 应可被消费，实际 %v", err)
	}
}

func nowUTC() time.Time { return time.Now() }

// ── 路由 CRUD ──

func TestRouteCRUD(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)

	// 建
	rec := r.do(t, http.MethodPost, "/api/v1/routes", map[string]any{
		"domain": "api.example.com", "upstream": "10.8.0.2:8080",
		"block": "403", "body_max": "5MB", "compress": true,
		"wl": []string{"203.0.113.7"},
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("建路由应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 列
	_, data := envelope(t, r.do(t, http.MethodGet, "/api/v1/routes", nil, c))
	routes, _ := data["routes"].([]any)
	if len(routes) != 1 {
		t.Fatalf("应有 1 条路由，实际 %v", data)
	}

	// 改
	rec = r.do(t, http.MethodPut, "/api/v1/routes/api.example.com", map[string]any{
		"domain": "api.example.com", "upstream": "10.8.0.9:9090",
		"block": "abort", "body_max": "1MB",
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("改路由应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_, data = envelope(t, r.do(t, http.MethodGet, "/api/v1/routes", nil, c))
	got := data["routes"].([]any)[0].(map[string]any)
	if got["upstream"] != "10.8.0.9:9090" {
		t.Errorf("回源地址未更新: %v", got)
	}

	// 删
	if rec := r.do(t, http.MethodDelete, "/api/v1/routes/api.example.com", nil, c); rec.Code != http.StatusOK {
		t.Fatalf("删路由应成功，实际 %d", rec.Code)
	}
	if rec := r.do(t, http.MethodDelete, "/api/v1/routes/api.example.com", nil, c); rec.Code != http.StatusNotFound {
		t.Fatalf("删不存在的路由应 404，实际 %d", rec.Code)
	}
}

// 非法输入必须在入口拒绝。
//
// 非法值一旦入库，**每一次**下发都会失败，而错误出现在与那次配置操作完全
// 无关的时刻——排查时很难联想到是几天前某条路由填错了。
func TestRouteInputValidation(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)

	for name, body := range map[string]map[string]any{
		"域名为空":    {"domain": "", "upstream": "10.0.0.1:80"},
		"回源为空":    {"domain": "a.example.com", "upstream": ""},
		"回源缺端口":   {"domain": "a.example.com", "upstream": "10.0.0.1"},
		"未知处置方式":  {"domain": "a.example.com", "upstream": "10.0.0.1:80", "block": "reject"},
		"非法请求体上限": {"domain": "a.example.com", "upstream": "10.0.0.1:80", "body_max": "不是大小"},
		"非法白名单":   {"domain": "a.example.com", "upstream": "10.0.0.1:80", "wl": []string{"不是IP"}},
	} {
		if rec := r.do(t, http.MethodPost, "/api/v1/routes", body, c); rec.Code != http.StatusBadRequest {
			t.Errorf("%s 应返回 400，实际 %d: %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestDuplicateDomainRejected(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	body := map[string]any{"domain": "api.example.com", "upstream": "10.0.0.1:80"}
	if rec := r.do(t, http.MethodPost, "/api/v1/routes", body, c); rec.Code != http.StatusOK {
		t.Fatalf("首次建应成功: %s", rec.Body.String())
	}
	if rec := r.do(t, http.MethodPost, "/api/v1/routes", body, c); rec.Code != http.StatusConflict {
		t.Fatalf("重名应返回 409，实际 %d", rec.Code)
	}
}

// 没有任何节点在线时，下发必须失败而不是「成功推给 0 个节点」。
//
// 报告「成功」会让人以为配置已经生效了，而实际上一个节点都没收到——
// 这类假成功比失败危险得多。
func TestDeployWithNoNodesFails(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	_ = r.do(t, http.MethodPost, "/api/v1/routes",
		map[string]any{"domain": "api.example.com", "upstream": "10.0.0.1:80"}, c)

	rec := r.do(t, http.MethodPost, "/api/v1/deploys", map[string]any{"note": "试试"}, c)
	if rec.Code == http.StatusOK {
		t.Fatalf("没有在线节点时不应报告下发成功: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "节点") {
		t.Errorf("错误应说明是没有可下发的节点: %s", rec.Body.String())
	}
}

// 渲染不出来的配置不得触达节点。
func TestDeployRejectsUnrenderableConfig(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	// 绕过入口校验直接写一条坏数据，模拟「历史遗留的非法值」
	if err := r.st.PutRoute(context.Background(), model.Route{
		Domain: "bad.example.com", Upstream: "10.0.0.1:80",
		Block: model.BlockAbort, BodyMax: "不是大小",
	}); err != nil {
		t.Fatal(err)
	}
	rec := r.do(t, http.MethodPost, "/api/v1/deploys", nil, c)
	if rec.Code == http.StatusOK {
		t.Fatal("渲染失败时不应报告下发成功")
	}
}

// ── 草稿与勾选下发 ──

// 草稿全局可见：任何人都能看到别人正在改什么。
func TestDraftsAreGloballyVisible(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)

	// 模拟 ops-bot 写的草稿
	if err := r.st.PutDraft(context.Background(), "route:b.example.com",
		map[string]any{"upstream": "10.0.0.9:80"}, "ops-bot", time.Now()); err != nil {
		t.Fatal(err)
	}
	// 当前操作人自己写一条
	if rec := r.do(t, http.MethodPut, "/api/v1/drafts/route:a.example.com",
		map[string]any{"patch": map[string]any{"upstream": "10.0.0.1:80"}}, c); rec.Code != http.StatusOK {
		t.Fatalf("写草稿应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}

	_, data := envelope(t, r.do(t, http.MethodGet, "/api/v1/drafts", nil, c))
	ds, _ := data["drafts"].([]any)
	if len(ds) != 2 {
		t.Fatalf("应看到全部 2 条草稿（含别人的），实际 %v", data)
	}
	// 作者要带回来：确认弹层要逐条标注是谁改的
	authors := map[string]bool{}
	for _, d := range ds {
		authors[d.(map[string]any)["updated_by"].(string)] = true
	}
	if !authors["ops-bot"] || !authors[auth.AdminUser] {
		t.Errorf("草稿应带上作者，实际 %v", authors)
	}
}

// 空 patch 表示该资源已无待下发改动，应删除而不是存一个空对象。
func TestEmptyPatchClearsDraft(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	key := "route:a.example.com"
	_ = r.do(t, http.MethodPut, "/api/v1/drafts/"+key,
		map[string]any{"patch": map[string]any{"upstream": "10.0.0.1:80"}}, c)
	_ = r.do(t, http.MethodPut, "/api/v1/drafts/"+key, map[string]any{"patch": map[string]any{}}, c)

	_, data := envelope(t, r.do(t, http.MethodGet, "/api/v1/drafts", nil, c))
	if ds, _ := data["drafts"].([]any); len(ds) != 0 {
		t.Fatalf("空 patch 应清除草稿，实际还剩 %v", ds)
	}
}

// 下发只携带**本次勾选**的资源，未勾选的草稿必须原样留着。
//
// 这是「推送时勾选」那个决定的核心：若下发顺手把全部草稿一起推了、或顺手清空，
// 别人还没推的改动就被无声吞掉了——而他不会知道。
func TestDeployOnlyCarriesSelectedResources(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	ctx := context.Background()

	for _, d := range []string{"a.example.com", "b.example.com"} {
		if err := r.st.PutRoute(ctx, model.Route{
			Domain: d, Upstream: "10.0.0.1:80", Block: model.BlockAbort, BodyMax: "1MB",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 两条草稿，分属不同的人
	_ = r.st.PutDraft(ctx, "route:a.example.com", map[string]any{"upstream": "10.0.0.2:80"}, auth.AdminUser, time.Now())
	_ = r.st.PutDraft(ctx, "route:b.example.com", map[string]any{"upstream": "10.0.0.3:80"}, "ops-bot", time.Now())

	// 只勾 a
	rec := r.do(t, http.MethodPost, "/api/v1/deploys",
		map[string]any{"res_keys": []string{"route:a.example.com"}}, c)
	// 没有在线节点，下发会失败——但**草稿的处理不该受此影响**之外，
	// 更重要的是它不能把 b 的草稿也带走。这里只断言 b 还在。
	_ = rec

	left, err := r.st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range left {
		if d.ResKey == "route:b.example.com" {
			return // 未勾选的草稿仍在，符合预期
		}
	}
	t.Fatalf("未勾选的草稿被吞掉了，剩余 %+v", left)
}

// 预览返回**后端权威渲染**的两侧：当前基线与勾选草稿合入后的结果。
//
// ADR-0007 把真相放在确认弹层：工作台右栏那份可读表示不是下发内容，只有这里
// 返回的字节才是。两侧都由后端渲染，前端只负责比对——否则「所见即所发」
// 这个性质在任何一侧都立不住。
func TestPreviewReturnsAuthoritativeBothSides(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	ctx := context.Background()

	if err := r.st.PutRoute(ctx, model.Route{
		Domain: "api.example.com", Upstream: "10.8.0.2:8080",
		Block: model.BlockAbort, BodyMax: "5MB",
	}); err != nil {
		t.Fatal(err)
	}
	// 一条改回源的草稿
	if err := r.st.PutDraft(ctx, "route:api.example.com",
		map[string]any{"upstream": "10.0.0.9:9090"}, auth.AdminUser, time.Now()); err != nil {
		t.Fatal(err)
	}

	rec := r.do(t, http.MethodPost, "/api/v1/config/preview",
		map[string]any{"res_keys": []string{"route:api.example.com"}}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("预览应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_, data := envelope(t, rec)

	cur, _ := data["current"].(string)
	next, _ := data["next"].(string)
	if cur == "" || next == "" {
		t.Fatalf("应返回当前与合入草稿后的两份渲染，实际 %v", data)
	}
	if cur == next {
		t.Fatal("草稿改了回源，两侧不应相同")
	}
	if !strings.Contains(cur, "10.8.0.2:8080") {
		t.Errorf("当前侧应是线上值: %s", cur)
	}
	if !strings.Contains(next, "10.0.0.9:9090") {
		t.Errorf("合入侧应含草稿值: %s", next)
	}
	// 两侧都必须是后端渲染器的产物——含它特有的 handler 结构
	if !strings.Contains(next, "reverse_proxy") || !strings.Contains(next, "max_size") {
		t.Errorf("预览应是真实可下发的 Caddy 配置: %s", next)
	}
}

// 未勾选的草稿不得进入预览。
//
// 预览要与「点确认后真正下发的东西」逐字一致；把没勾的也算进来，
// 用户批准的就不是他实际推出去的那份。
func TestPreviewExcludesUnselectedDrafts(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	ctx := context.Background()

	for _, d := range []string{"a.example.com", "b.example.com"} {
		_ = r.st.PutRoute(ctx, model.Route{
			Domain: d, Upstream: "10.0.0.1:80", Block: model.BlockAbort, BodyMax: "1MB",
		})
	}
	_ = r.st.PutDraft(ctx, "route:a.example.com", map[string]any{"upstream": "10.0.0.2:80"}, "abiu", time.Now())
	_ = r.st.PutDraft(ctx, "route:b.example.com", map[string]any{"upstream": "10.0.0.3:80"}, "ops-bot", time.Now())

	_, data := envelope(t, r.do(t, http.MethodPost, "/api/v1/config/preview",
		map[string]any{"res_keys": []string{"route:a.example.com"}}, c))
	next, _ := data["next"].(string)

	if !strings.Contains(next, "10.0.0.2:80") {
		t.Errorf("勾选的草稿应出现在预览里: %s", next)
	}
	if strings.Contains(next, "10.0.0.3:80") {
		t.Error("未勾选的草稿不应进入预览——用户批准的必须就是他实际推出去的那份")
	}
}

// decodeData 取出 {code,data,msg} 包裹里的 data。
func decodeData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
		Msg  string         `json:"msg"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是合法 JSON: %v，原文 %s", err, w.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("业务码非 0：%d %s", env.Code, env.Msg)
	}
	return env.Data
}
