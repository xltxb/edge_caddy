package agent_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/caddytest"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
)

// echoUpstream 把 X-Verified-Sub 回显出来，用来证明验签结果真的透传到了源站。
func echoUpstream(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "UPSTREAM sub=%s", r.Header.Get("X-Verified-Sub"))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// serveVerify 把 Agent 的校验端点挂到 fixture 给的 unix socket 上，
// 返回一个能把它「杀掉」的函数——ADR-0003 的第四行要验的就是那个情形。
func serveVerify(t *testing.T, c *caddytest.Caddy, rules []model.VerifyRule) func() {
	t.Helper()
	v := agent.NewVerifyServer(nil)
	v.SetRules(rules)

	ln, err := net.Listen("unix", c.VerifySocketPath())
	if err != nil {
		t.Fatalf("监听校验端点: %v", err)
	}
	srv := &http.Server{Handler: v.Handler()}
	go func() { _ = srv.Serve(ln) }()

	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = srv.Close()
		_ = ln.Close()
	}
	t.Cleanup(stop)
	return stop
}

func applyWithRules(t *testing.T, c *caddytest.Caddy, routes []model.Route, rules []model.Rule) {
	t.Helper()
	cfg, issues := render.Render(routes, rules, render.Options{
		HTTPListen: c.EdgeListen(), VerifyAddr: c.VerifyDial(),
	})
	if len(issues) > 0 {
		t.Fatalf("渲染报了校验问题: %v", issues)
	}
	if _, err := agent.NewCaddyClient(c.AdminURL()).ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("Caddy 拒绝了带访问规则的配置: %v", err)
	}
}

func signHMAC(secret, method, uri string, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s.%s.%s", ts, method, uri)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// ADR-0003 的实测表，逐行复现。
//
// 官方 Caddy 既没有 JWT 模块也没有 HMAC 模块，所以受保护域名的请求经
// forward_auth 委托给 Agent 在回环上的校验端点，由 Agent 用 Go 真正验签。
//
//	无凭据          → 403
//	错凭据          → 403
//	正确凭据        → 200，且 X-Verified-Sub 透传到了上游
//	校验端点被杀后  → 502（带正确凭据也是 502）
func TestServiceSecretMatchesADR0003Table(t *testing.T) {
	const secret = "s3cr3t-shared-key"
	up := echoUpstream(t)
	c := caddytest.New(t)

	rule := model.Rule{
		ID: "svc-key-1", Name: "服务密钥", Type: model.RuleServiceSecret,
		Enabled: true, ApplyTo: []string{"api.example.com"}, Secret: secret,
		Spec: model.RuleSpec{
			Header: "X-Service-Key", Algo: "hmac-sha256",
			TTLSeconds: 300, ReplayProtection: false,
		},
	}
	stop := serveVerify(t, c, render.VerifyRules([]model.Rule{rule}))
	applyWithRules(t, c,
		[]model.Route{{Domain: "api.example.com", Upstream: up, BlockMode: model.BlockAbort}},
		[]model.Rule{rule})

	t.Run("无凭据 → 403", func(t *testing.T) {
		if code, _ := c.Get("api.example.com", "/", nil); code != 403 {
			t.Fatalf("得到 %d，想要 403", code)
		}
	})

	t.Run("错凭据 → 403", func(t *testing.T) {
		bad := signHMAC("wrong-key", "GET", "/", time.Now())
		if code, _ := c.Get("api.example.com", "/", map[string]string{"X-Service-Key": bad}); code != 403 {
			t.Fatalf("得到 %d，想要 403", code)
		}
	})

	t.Run("过期的时间戳 → 403", func(t *testing.T) {
		old := signHMAC(secret, "GET", "/", time.Now().Add(-time.Hour))
		if code, _ := c.Get("api.example.com", "/", map[string]string{"X-Service-Key": old}); code != 403 {
			t.Fatalf("得到 %d，想要 403", code)
		}
	})

	t.Run("正确凭据 → 200", func(t *testing.T) {
		good := signHMAC(secret, "GET", "/", time.Now())
		code, body := c.Get("api.example.com", "/", map[string]string{"X-Service-Key": good})
		if code != 200 {
			t.Fatalf("得到 %d %q，想要 200", code, body)
		}
		if !strings.HasPrefix(body, "UPSTREAM") {
			t.Fatalf("响应体 = %q，请求应当被转发到上游", body)
		}
	})

	t.Run("校验端点被杀 → 502 且带正确凭据也进不去", func(t *testing.T) {
		stop()
		good := signHMAC(secret, "GET", "/", time.Now())
		code, _ := c.Get("api.example.com", "/", map[string]string{"X-Service-Key": good})
		if code != 502 {
			// fail-closed：Agent 挂掉时受保护域名整体 502，不会被绕过。
			// 代价是 Agent 的存活成为受保护域名的硬依赖——部署脚本里的
			// Restart=always 因此不是锦上添花，而是承重的。
			t.Fatalf("得到 %d，想要 502（fail-closed）", code)
		}
	})
}

// 签名把请求方法与 URI 纳入计算：一条截获的签名不能被换到别的路径上重放。
func TestServiceSecretSignatureIsBoundToMethodAndURI(t *testing.T) {
	const secret = "s3cr3t-shared-key"
	up := echoUpstream(t)
	c := caddytest.New(t)

	rule := model.Rule{
		ID: "svc-key-1", Type: model.RuleServiceSecret, Enabled: true,
		ApplyTo: []string{"api.example.com"}, Secret: secret,
		Spec: model.RuleSpec{Header: "X-Service-Key", TTLSeconds: 300},
	}
	serveVerify(t, c, render.VerifyRules([]model.Rule{rule}))
	applyWithRules(t, c,
		[]model.Route{{Domain: "api.example.com", Upstream: up, BlockMode: model.BlockAbort}},
		[]model.Rule{rule})

	// 为 /public 签的名，拿去打 /admin。
	sig := signHMAC(secret, "GET", "/public", time.Now())
	if code, _ := c.Get("api.example.com", "/admin", map[string]string{"X-Service-Key": sig}); code != 403 {
		t.Fatalf("换路径重放得到 %d，想要 403", code)
	}
	if code, _ := c.Get("api.example.com", "/public", map[string]string{"X-Service-Key": sig}); code != 200 {
		t.Fatalf("同一条签名打它自己的路径应当放行，得到 %d", code)
	}
}

// 重放保护打开后，同一条签名只能用一次。
func TestReplayProtectionRejectsSecondUse(t *testing.T) {
	const secret = "s3cr3t-shared-key"
	up := echoUpstream(t)
	c := caddytest.New(t)

	rule := model.Rule{
		ID: "svc-key-1", Type: model.RuleServiceSecret, Enabled: true,
		ApplyTo: []string{"api.example.com"}, Secret: secret,
		Spec: model.RuleSpec{Header: "X-Service-Key", TTLSeconds: 300, ReplayProtection: true},
	}
	serveVerify(t, c, render.VerifyRules([]model.Rule{rule}))
	applyWithRules(t, c,
		[]model.Route{{Domain: "api.example.com", Upstream: up, BlockMode: model.BlockAbort}},
		[]model.Rule{rule})

	sig := signHMAC(secret, "GET", "/", time.Now())
	if code, _ := c.Get("api.example.com", "/", map[string]string{"X-Service-Key": sig}); code != 200 {
		t.Fatalf("第一次使用应当放行，得到 %d", code)
	}
	if code, _ := c.Get("api.example.com", "/", map[string]string{"X-Service-Key": sig}); code != 403 {
		t.Fatalf("同一条签名第二次使用应当被拒，得到 %d", code)
	}
}

// JWT：真验签，且 sub 透传到上游 —— 那是「边缘只做格式过滤」给不了的。
func TestJWTVerificationAndClaimPassthrough(t *testing.T) {
	up := echoUpstream(t)
	c := caddytest.New(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kty": "EC", "crv": "P-256", "kid": "k1",
			"x": b64u(key.PublicKey.X), "y": b64u(key.PublicKey.Y),
		}}})
	}))
	t.Cleanup(jwks.Close)

	rule := model.Rule{
		ID: "jwt-1", Type: model.RuleJWTBearer, Enabled: true,
		ApplyTo: []string{"api.example.com"},
		Spec: model.RuleSpec{
			Issuer: "https://idp.internal/", Audience: "edge",
			JWKSURL: jwks.URL, SkewSeconds: 60,
		},
	}
	serveVerify(t, c, render.VerifyRules([]model.Rule{rule}))
	applyWithRules(t, c,
		[]model.Route{{Domain: "api.example.com", Upstream: up, BlockMode: model.BlockAbort}},
		[]model.Rule{rule})

	mint := func(claims jwt.MapClaims) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		tok.Header["kid"] = "k1"
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	t.Run("无 token → 403", func(t *testing.T) {
		if code, _ := c.Get("api.example.com", "/", nil); code != 403 {
			t.Fatalf("得到 %d", code)
		}
	})

	t.Run("签名瞎编的串 → 403", func(t *testing.T) {
		// 「边缘只做格式前置过滤」的方案对这个是放行的 —— 那正是它被否掉的理由。
		code, _ := c.Get("api.example.com", "/",
			map[string]string{"Authorization": "Bearer eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJ4In0.bogus"})
		if code != 403 {
			t.Fatalf("得到 %d，想要 403", code)
		}
	})

	t.Run("aud 不对 → 403", func(t *testing.T) {
		tok := mint(jwt.MapClaims{
			"iss": "https://idp.internal/", "aud": "别的系统",
			"sub": "user-42", "exp": time.Now().Add(time.Hour).Unix(),
		})
		if code, _ := c.Get("api.example.com", "/", map[string]string{"Authorization": "Bearer " + tok}); code != 403 {
			t.Fatalf("得到 %d", code)
		}
	})

	t.Run("已过期 → 403", func(t *testing.T) {
		tok := mint(jwt.MapClaims{
			"iss": "https://idp.internal/", "aud": "edge",
			"sub": "user-42", "exp": time.Now().Add(-time.Hour).Unix(),
		})
		if code, _ := c.Get("api.example.com", "/", map[string]string{"Authorization": "Bearer " + tok}); code != 403 {
			t.Fatalf("得到 %d", code)
		}
	})

	t.Run("有效 token → 200 且 sub 透传到上游", func(t *testing.T) {
		tok := mint(jwt.MapClaims{
			"iss": "https://idp.internal/", "aud": "edge",
			"sub": "user-42", "exp": time.Now().Add(time.Hour).Unix(),
		})
		code, body := c.Get("api.example.com", "/", map[string]string{"Authorization": "Bearer " + tok})
		if code != 200 {
			t.Fatalf("得到 %d %q", code, body)
		}
		if body != "UPSTREAM sub=user-42" {
			// 源站不必重新解析 token —— 这是格式过滤方案给不了的（ADR-0003 实测）。
			t.Fatalf("响应体 = %q，想要 sub 被透传", body)
		}
	})
}

// 未绑定域名的规则不生效 —— 那是半成品状态，不是「对所有域名生效」。
func TestUnboundRuleDoesNotProtectAnything(t *testing.T) {
	up := echoUpstream(t)
	c := caddytest.New(t)

	rule := model.Rule{
		ID: "svc-key-1", Type: model.RuleServiceSecret, Enabled: true,
		ApplyTo: nil, Secret: "k",
		Spec: model.RuleSpec{Header: "X-Service-Key", TTLSeconds: 300},
	}
	serveVerify(t, c, render.VerifyRules([]model.Rule{rule}))
	applyWithRules(t, c,
		[]model.Route{{Domain: "api.example.com", Upstream: up, BlockMode: model.BlockAbort}},
		[]model.Rule{rule})

	if code, _ := c.Get("api.example.com", "/", nil); code != 200 {
		t.Fatalf("未绑定域名的规则不该保护任何东西，得到 %d", code)
	}
}

// 停用的规则同样不生效。
func TestDisabledRuleDoesNotProtect(t *testing.T) {
	up := echoUpstream(t)
	c := caddytest.New(t)

	rule := model.Rule{
		ID: "svc-key-1", Type: model.RuleServiceSecret, Enabled: false,
		ApplyTo: []string{"api.example.com"}, Secret: "k",
		Spec: model.RuleSpec{Header: "X-Service-Key", TTLSeconds: 300},
	}
	serveVerify(t, c, render.VerifyRules([]model.Rule{rule}))
	applyWithRules(t, c,
		[]model.Route{{Domain: "api.example.com", Upstream: up, BlockMode: model.BlockAbort}},
		[]model.Rule{rule})

	if code, _ := c.Get("api.example.com", "/", nil); code != 200 {
		t.Fatalf("停用的规则不该生效，得到 %d", code)
	}
}

// 未受保护的域名不经过校验端点，因此不受 Agent 存活影响（ADR-0003）。
func TestUnprotectedDomainSurvivesVerifyEndpointDeath(t *testing.T) {
	up := echoUpstream(t)
	c := caddytest.New(t)

	rule := model.Rule{
		ID: "svc-key-1", Type: model.RuleServiceSecret, Enabled: true,
		ApplyTo: []string{"guarded.example.com"}, Secret: "k",
		Spec: model.RuleSpec{Header: "X-Service-Key", TTLSeconds: 300},
	}
	stop := serveVerify(t, c, render.VerifyRules([]model.Rule{rule}))
	applyWithRules(t, c, []model.Route{
		{Domain: "guarded.example.com", Upstream: up, BlockMode: model.BlockAbort},
		{Domain: "open.example.com", Upstream: up, BlockMode: model.BlockAbort},
	}, []model.Rule{rule})

	stop()
	if code, _ := c.Get("guarded.example.com", "/", nil); code != 502 {
		t.Errorf("受保护域名应当 502，得到 %d", code)
	}
	if code, _ := c.Get("open.example.com", "/", nil); code != 200 {
		t.Errorf("未受保护的域名不该受影响，得到 %d", code)
	}
}

// 共享密钥不进 Caddy 配置 —— Admin API 能读回整份运行配置。
func TestSecretNeverEntersCaddyConfig(t *testing.T) {
	const secret = "s3cr3t-shared-key"
	up := echoUpstream(t)
	c := caddytest.New(t)

	rule := model.Rule{
		ID: "svc-key-1", Type: model.RuleServiceSecret, Enabled: true,
		ApplyTo: []string{"api.example.com"}, Secret: secret,
		Spec: model.RuleSpec{Header: "X-Service-Key", TTLSeconds: 300},
	}
	cfg, issues := render.Render(
		[]model.Route{{Domain: "api.example.com", Upstream: up, BlockMode: model.BlockAbort}},
		[]model.Rule{rule},
		render.Options{HTTPListen: c.EdgeListen(), VerifyAddr: c.VerifyDial()})
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	if strings.Contains(string(cfg), secret) {
		t.Fatal("共享密钥出现在了 Caddy 配置里 —— Admin API 能把它读回去")
	}
	if !strings.Contains(string(cfg), "/verify/svc-key-1") {
		t.Fatal("配置里应当只有委托路径，没有密钥")
	}

	// 而它必须出现在走旁路的那份里，否则校验端点验不了签。
	vr, _ := json.Marshal(render.VerifyRules([]model.Rule{rule}))
	if !strings.Contains(string(vr), secret) {
		t.Fatal("旁路的校验规则里应当带密钥")
	}
}

func b64u(n *big.Int) string {
	b := n.Bytes()
	// P-256 的坐标固定 32 字节，短了要左补零，否则某些解析器会算错。
	for len(b) < 32 {
		b = append([]byte{0}, b...)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
