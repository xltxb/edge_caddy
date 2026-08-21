package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
)

const opsBotToken = "bot-token-for-tests"

func newServer(t *testing.T) (*gin.Engine, *store.Store) {
	t.Helper()
	st := testdb.New(t)
	if err := st.CreateUser(context.Background(), "abiu", "correct-horse"); err != nil {
		t.Fatalf("建账号: %v", err)
	}
	r := api.New(api.Options{
		Store:       st,
		SessionTTL:  time.Hour,
		OpsBotToken: opsBotToken,
	})
	return r, st
}

type envelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
	Msg  string          `json:"msg"`
}

func do(t *testing.T, r *gin.Engine, method, path string, body any, mod func(*http.Request)) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if mod != nil {
		mod(req)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是合法的包裹体: %v\nbody=%s", err, w.Body.String())
	}
	return w, env
}

func login(t *testing.T, r *gin.Engine) *http.Cookie {
	t.Helper()
	w, env := do(t, r, "POST", "/api/v1/auth/login",
		map[string]string{"username": "abiu", "password": "correct-horse"}, nil)
	if w.Code != http.StatusOK || env.Code != api.CodeOK {
		t.Fatalf("登录失败：http=%d code=%d msg=%s", w.Code, env.Code, env.Msg)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "ec_session" {
			return c
		}
	}
	t.Fatal("登录响应里没有 ec_session Cookie")
	return nil
}

// --- 契约 §0.2：HTTP 状态码与 code 的分工 ---

// 未登录必须是 HTTP 401，不能是 200 + 某个 code。
// 401 是前端 http.ts 里唯一特判的码，用别的表达会让它跳不了登录页。
func TestUnauthenticatedIsHTTP401(t *testing.T) {
	r, _ := newServer(t)
	w, _ := do(t, r, "GET", "/api/v1/auth/session", nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录访问 /auth/session 得到 http=%d，想要 401", w.Code)
	}
}

// 端点不存在必须是 HTTP 404，而不是 CodeNotFound(1003)。
// 1003 表示**资源**不存在；混在一起前端就分不清「路由写错了」和
// 「这条路由被别人删了」。
func TestUnknownEndpointIsHTTP404NotCode1003(t *testing.T) {
	r, _ := newServer(t)
	w, env := do(t, r, "GET", "/api/v1/nope", nil, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("未知端点得到 http=%d，想要 404", w.Code)
	}
	if env.Code == api.CodeNotFound {
		t.Fatalf("未知端点不该用 CodeNotFound(%d)——那是资源不存在的码", api.CodeNotFound)
	}
}

// 业务失败走 HTTP 200 + 非零 code，且 msg 是给人看的中文。
func TestBusinessFailureIsHTTP200WithCode(t *testing.T) {
	r, _ := newServer(t)
	w, env := do(t, r, "POST", "/api/v1/auth/login",
		map[string]string{"username": "abiu", "password": "wrong"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("口令错误得到 http=%d，想要 200（业务失败走 code 而不是状态码）", w.Code)
	}
	if env.Code != api.CodeBadParam {
		t.Fatalf("code=%d，想要 %d", env.Code, api.CodeBadParam)
	}
	if env.Msg == "" {
		t.Error("业务失败必须带用户可读的 msg，前端会直接进 toast")
	}
	if string(env.Data) != "null" {
		t.Errorf("失败响应的 data 应为 null，实际 %s", env.Data)
	}
}

// 登录失败不区分「用户名不存在」与「口令错误」——区分了就等于
// 提供了一个用户名枚举接口（契约 §1）。
func TestLoginDoesNotRevealWhetherUserExists(t *testing.T) {
	r, _ := newServer(t)
	_, wrongPw := do(t, r, "POST", "/api/v1/auth/login",
		map[string]string{"username": "abiu", "password": "wrong"}, nil)
	_, noSuchUser := do(t, r, "POST", "/api/v1/auth/login",
		map[string]string{"username": "nobody", "password": "wrong"}, nil)

	if wrongPw.Code != noSuchUser.Code || wrongPw.Msg != noSuchUser.Msg {
		t.Fatalf("两种失败可区分：口令错=(%d,%q) 用户不存在=(%d,%q)",
			wrongPw.Code, wrongPw.Msg, noSuchUser.Code, noSuchUser.Msg)
	}
}

// --- 会话 ---

func TestLoginThenSession(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)

	if !ck.HttpOnly {
		t.Error("会话 Cookie 必须是 HttpOnly")
	}
	if ck.SameSite != http.SameSiteStrictMode {
		t.Errorf("会话 Cookie 的 SameSite=%v，想要 Strict", ck.SameSite)
	}

	w, env := do(t, r, "GET", "/api/v1/auth/session", nil, func(req *http.Request) {
		req.AddCookie(ck)
	})
	if w.Code != http.StatusOK || env.Code != api.CodeOK {
		t.Fatalf("带 Cookie 访问 session：http=%d code=%d", w.Code, env.Code)
	}
	var p struct {
		Username string `json:"username"`
		Kind     string `json:"kind"`
	}
	if err := json.Unmarshal(env.Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.Username != "abiu" || p.Kind != "human" {
		t.Errorf("身份 = %+v，想要 {abiu human}", p)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)

	if w, _ := do(t, r, "POST", "/api/v1/auth/logout", nil, func(req *http.Request) {
		req.AddCookie(ck)
	}); w.Code != http.StatusOK {
		t.Fatalf("登出 http=%d", w.Code)
	}

	// 会话必须在服务端失效，而不只是让浏览器丢掉 Cookie——
	// 会话落库的全部意义就在这里（迁移里 sessions 表上方那段注释）。
	w, _ := do(t, r, "GET", "/api/v1/auth/session", nil, func(req *http.Request) {
		req.AddCookie(ck)
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("登出后旧 Cookie 仍然可用：http=%d", w.Code)
	}
}

// --- ops-bot ---

func TestOpsBotBearerIsAccepted(t *testing.T) {
	r, _ := newServer(t)
	w, env := do(t, r, "GET", "/api/v1/auth/session", nil, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+opsBotToken)
	})
	if w.Code != http.StatusOK {
		t.Fatalf("ops-bot 带正确 Bearer 得到 http=%d", w.Code)
	}
	var p struct {
		Username string `json:"username"`
		Kind     string `json:"kind"`
	}
	_ = json.Unmarshal(env.Data, &p)
	if p.Username != "ops-bot" || p.Kind != "bot" {
		t.Errorf("身份 = %+v，想要 {ops-bot bot}", p)
	}
}

func TestWrongBearerIs401(t *testing.T) {
	r, _ := newServer(t)
	w, _ := do(t, r, "GET", "/api/v1/auth/session", nil, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer nope")
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("错误 Bearer 得到 http=%d，想要 401", w.Code)
	}
}

// --- 审计 ---

// 登录成功与失败都要留痕，且 action 用契约 §5 的措辞。
func TestLoginIsAudited(t *testing.T) {
	r, st := newServer(t)
	ctx := context.Background()

	login(t, r)
	do(t, r, "POST", "/api/v1/auth/login",
		map[string]string{"username": "abiu", "password": "wrong"}, nil)
	// 不存在的用户名也要留痕——那正是「有人在枚举用户名」的信号。
	do(t, r, "POST", "/api/v1/auth/login",
		map[string]string{"username": "mallory", "password": "x"}, nil)

	rows, err := st.Pool.Query(ctx,
		`SELECT operator || '/' || action, result::text FROM audit_logs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got [][2]string
	for rows.Next() {
		var a, res string
		if err := rows.Scan(&a, &res); err != nil {
			t.Fatal(err)
		}
		got = append(got, [2]string{a, res})
	}
	// operator 记的是被尝试的用户名，不是 "-"。登录发生在鉴权之前，
	// 中间件那时没有 principal；不显式补上的话，审计页对失败登录的提示
	// 就说不出「谁在试」。
	want := [][2]string{{"abiu/登录", "ok"}, {"abiu/登录", "fail"}, {"mallory/登录", "fail"}}
	if len(got) != len(want) {
		t.Fatalf("审计行 = %v，想要 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("审计行[%d] = %v，想要 %v", i, got[i], want[i])
		}
	}
}
