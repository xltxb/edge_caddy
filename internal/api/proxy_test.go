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

// **生产里 XFF 从来不是一条，而是一条链——而链是访问者能往里塞东西的。**
//
// 上面两条测的都是「XFF 只有一个值」。真实形状是这样的：
//
//	访问者发           X-Forwarded-For: 1.2.3.4        ← 伪造的
//	CDN 追加真实来源   X-Forwarded-For: 1.2.3.4, 203.0.113.9
//	nginx 追加对端     X-Forwarded-For: 1.2.3.4, 203.0.113.9, 172.71.x.x
//
// **左边是访问者写的，右边是每一跳自己写的。** 所以采信必须从右往左，
// 取第一个不属于可信代理的那个 —— 而不是取第一个。
//
// 取第一个的话，任何人发一个 XFF 就能让审计记下任意 IP，而这条链路上
// 每一跳都工作正常、日志里也看不出异样。**这是「成功的假象」里最贵的一种**：
// 审计是 ADR-0013 准入模型的三分之一，而它记下的是攻击者选的值。
func TestForgedXFFAtHeadOfChainIsNotHonored(t *testing.T) {
	r, st := newServerWithProxies(t, []string{"127.0.0.1"})
	loginFrom(t, r, "127.0.0.1:5555", "1.2.3.4, 203.0.113.9")

	ip := lastAuditIP(t, st)
	if ip == "1.2.3.4" {
		t.Fatal("采信了链首那个访问者自己写的值 —— " +
			"任何人发一个 X-Forwarded-For 就能决定审计里记谁")
	}
	if ip != "203.0.113.9" {
		t.Errorf("应当取链上最后一个非可信代理的地址，实际 %q", ip)
	}
}

// **反代应当只转一个值，而不是一条链。**
//
// 上面那条证明了「链能被正确解读」，这条说的是更强的一层：
// nginx 配 `X-Forwarded-For $remote_addr`（替换，不是追加）之后，
// 主控收到的就只有一个值，链上根本没有访问者写的部分可解读。
//
// 两者的区别不在结果，在**失败面**：解读一条链要求每一跳的可信名单都对，
// 名单漏一个就会取到攻击者写的值；而只有一个值时没有什么可漏的。
//
// 这跟今天那条「选让问题不存在，而不是把问题处理对」是同一条。
func TestSingleValueFromProxyNeedsNoChainWalking(t *testing.T) {
	r, st := newServerWithProxies(t, []string{"127.0.0.1"})
	loginFrom(t, r, "127.0.0.1:5555", "203.0.113.9")

	if ip := lastAuditIP(t, st); ip != "203.0.113.9" {
		t.Errorf("反代只转一个值时应当直接采信，实际 %q", ip)
	}
}
