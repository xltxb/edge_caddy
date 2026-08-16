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

	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/auth"
	"github.com/xltxb/edge_caddy/internal/enroll"
	"github.com/xltxb/edge_caddy/internal/store"
)

const testPassword = "correct horse battery staple"

type rig struct {
	h  http.Handler
	st *store.Store
	au *auth.Manager
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
	h := api.New(api.Deps{Store: st, Auth: au, Enroll: enroll.New(st)})
	return &rig{h: h, st: st, au: au}
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
