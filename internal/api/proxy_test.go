package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
)

func newServerWithProxies(t *testing.T, trusted []string) (*gin.Engine, *store.Store) {
	t.Helper()
	st := testdb.New(t)
	if err := st.CreateUser(context.Background(), "abiu", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	r := api.New(api.Options{
		Store: st, SessionTTL: time.Hour, TrustedProxies: trusted,
	})
	return r, st
}

// 登录一次并返回审计里记下的来源 IP。
func loginFrom(t *testing.T, r *gin.Engine, remoteAddr, xff string) string {
	t.Helper()
	body := `{"username":"abiu","password":"correct-horse"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("登录失败：%d %s", w.Code, w.Body.String())
	}
	return ""
}

// **默认谁也不信：任何人发一个 X-Forwarded-For 都伪造不了审计里的来源 IP。**
//
// gin 不调 SetTrustedProxies 时信任所有代理——实测过，那时
// `X-Forwarded-For: 1.2.3.4` 会原样进审计。
//
// 而审计是 ADR-0013 准入模型的三分之一（只绑内网 + Cookie + 全写审计）：
// **一个可以被访问者伪造的来源 IP，等于那三分之一在追责时不作数。**
func TestForgedXFFIsIgnoredWithoutTrustedProxies(t *testing.T) {
	r, st := newServerWithProxies(t, nil)
	loginFrom(t, r, "127.0.0.1:5555", "1.2.3.4")

	ip := lastAuditIP(t, st)
	if ip == "1.2.3.4" {
		t.Fatal("伪造的 X-Forwarded-For 进了审计 —— 来源 IP 不可信，" +
			"而审计是准入模型的三分之一")
	}
	if ip != "127.0.0.1" {
		t.Errorf("应当记下真实对端地址，实际 %q", ip)
	}
}

// **配了可信代理之后，它发来的 XFF 才作数。**
//
// 这是前置 nginx 时必须的：不采信 XFF 的话，审计里的来源 IP 会**全部**
// 变成反代自己的地址——那比伪造更彻底，因为它连一次都不对。
func TestXFFIsHonoredFromTrustedProxy(t *testing.T) {
	r, st := newServerWithProxies(t, []string{"127.0.0.1"})
	loginFrom(t, r, "127.0.0.1:5555", "203.0.113.9")

	if ip := lastAuditIP(t, st); ip != "203.0.113.9" {
		t.Errorf("可信代理转来的来源 IP 应当被采信，实际 %q", ip)
	}
}

// 可信代理配错了不该让主控起不来，但要退回「谁也不信」——
// **宁可记下反代的地址，也不能记下一个谁都能伪造的值**。
func TestBadTrustedProxyFallsBackToTrustingNobody(t *testing.T) {
	r, st := newServerWithProxies(t, []string{"这不是一个地址"})
	loginFrom(t, r, "127.0.0.1:5555", "1.2.3.4")

	if ip := lastAuditIP(t, st); ip == "1.2.3.4" {
		t.Error("配错时不该退回「信任所有代理」")
	}
}

func lastAuditIP(t *testing.T, st *store.Store) string {
	t.Helper()
	var ip *string
	err := st.Pool.QueryRow(context.Background(),
		`SELECT host(src_ip) FROM audit_logs ORDER BY id DESC LIMIT 1`).Scan(&ip)
	if err != nil {
		t.Fatalf("读审计: %v", err)
	}
	if ip == nil {
		return ""
	}
	return *ip
}
