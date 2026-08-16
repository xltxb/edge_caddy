package agent_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/model"
)

// idp 是一个真的 JWKS 服务：现场生成密钥对、真签 token、真暴露公钥。
//
// 不注入公钥而是起真服务，是因为「拉取 JWKS 并缓存」这一段正是生产里会出问题的
// 地方（IdP 慢、密钥轮换），注入公钥会把它整个跳过。
type idp struct {
	key    *rsa.PrivateKey
	kid    string
	server *httptest.Server
	hits   atomic.Int32
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{key: key, kid: "test-key-1"}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p.hits.Add(1)
		n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{
				{"kty": "RSA", "kid": p.kid, "use": "sig", "alg": "RS256", "n": n, "e": e},
			},
		})
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *idp) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = p.kid
	s, err := tok.SignedString(p.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func jwtRule(p *idp) model.AccessRule {
	return model.AccessRule{
		ID: "app-jwt", Type: model.RuleJWTBearer, Enabled: true,
		ApplyTo: []string{"api.example.com"},
		Spec: model.RuleSpec{
			Issuer: "https://idp.test", Audience: "edge-api",
			JWKS: p.server.URL, SkewSec: 60,
		},
	}
}

func callVerify(t *testing.T, v *agent.Verifier, host, authz string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	req.Host = host
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	v.ServeHTTP(rec, req)
	return rec.Code
}

// 合法 token 放行；**伪造签名必须被拒**。
//
// 后者是这整条链路存在的理由：上一版边缘只做格式检查，`Bearer eyJ.x.y` 这种
// 签名瞎编的串照样放行。这条测试就是那个缺陷的反面。
func TestJWTSignatureIsActuallyVerified(t *testing.T) {
	p := newIDP(t)
	v := agent.NewVerifier(nil)
	v.SetRules([]model.AccessRule{jwtRule(p)})

	good := p.sign(t, jwt.MapClaims{
		"iss": "https://idp.test", "aud": "edge-api", "sub": "user-42",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if code := callVerify(t, v, "api.example.com", "Bearer "+good); code != http.StatusOK {
		t.Fatalf("合法 token 应放行，实际 %d", code)
	}

	// 用另一把密钥签的 token——结构完全合法，只是签名对不上
	other := newIDP(t)
	forged := other.sign(t, jwt.MapClaims{
		"iss": "https://idp.test", "aud": "edge-api",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if code := callVerify(t, v, "api.example.com", "Bearer "+forged); code == http.StatusOK {
		t.Fatal("用别的密钥签的 token 必须被拒——这正是格式检查漏掉的那一类")
	}

	// 结构像 JWT 但完全瞎编的串
	if code := callVerify(t, v, "api.example.com", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln"); code == http.StatusOK {
		t.Fatal("瞎编签名的 token 必须被拒")
	}
}

// iss / aud / exp 都要真校验。
func TestJWTClaimsAreChecked(t *testing.T) {
	p := newIDP(t)
	v := agent.NewVerifier(nil)
	v.SetRules([]model.AccessRule{jwtRule(p)})

	cases := map[string]jwt.MapClaims{
		"签发者不对": {"iss": "https://evil.test", "aud": "edge-api", "exp": time.Now().Add(time.Hour).Unix()},
		"受众不对":  {"iss": "https://idp.test", "aud": "other-api", "exp": time.Now().Add(time.Hour).Unix()},
		"已过期":   {"iss": "https://idp.test", "aud": "edge-api", "exp": time.Now().Add(-time.Hour).Unix()},
	}
	for name, claims := range cases {
		if code := callVerify(t, v, "api.example.com", "Bearer "+p.sign(t, claims)); code == http.StatusOK {
			t.Errorf("%s 的 token 应被拒", name)
		}
	}
}

// 校验通过后把声明透传给源站：源站不必重新解析 token。
func TestVerifiedClaimsArePassedUpstream(t *testing.T) {
	p := newIDP(t)
	v := agent.NewVerifier(nil)
	v.SetRules([]model.AccessRule{jwtRule(p)})

	tok := p.sign(t, jwt.MapClaims{
		"iss": "https://idp.test", "aud": "edge-api", "sub": "user-42",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	req.Host = "api.example.com"
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	v.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Verified-Sub"); got != "user-42" {
		t.Fatalf("应把已验证的主体透传给源站，实际 %q", got)
	}
}

// JWKS 必须缓存：每个请求都去 IdP 取会把 IdP 打挂，也让边缘的可用性绑在它身上。
func TestJWKSIsCached(t *testing.T) {
	p := newIDP(t)
	v := agent.NewVerifier(nil)
	v.SetRules([]model.AccessRule{jwtRule(p)})

	tok := p.sign(t, jwt.MapClaims{
		"iss": "https://idp.test", "aud": "edge-api",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	for i := 0; i < 20; i++ {
		callVerify(t, v, "api.example.com", "Bearer "+tok)
	}
	if hits := p.hits.Load(); hits > 2 {
		t.Fatalf("JWKS 应被缓存，20 次请求只应拉取一两次，实际 %d 次", hits)
	}
}

// 未受保护的域名不经过校验：它们不该因为鉴权而变慢或变脆。
func TestUnprotectedHostIsAllowedWithoutCredentials(t *testing.T) {
	p := newIDP(t)
	v := agent.NewVerifier(nil)
	v.SetRules([]model.AccessRule{jwtRule(p)})

	if code := callVerify(t, v, "open.example.com", ""); code != http.StatusOK {
		t.Fatalf("未绑定规则的域名应直接放行，实际 %d", code)
	}
}

// 停用的规则必须真的不生效。
func TestDisabledRuleDoesNotProtect(t *testing.T) {
	p := newIDP(t)
	rule := jwtRule(p)
	rule.Enabled = false
	v := agent.NewVerifier(nil)
	v.SetRules([]model.AccessRule{rule})

	if code := callVerify(t, v, "api.example.com", ""); code != http.StatusOK {
		t.Fatalf("停用的规则不应拦截，实际 %d", code)
	}
}

// 缺少凭据被拒，且不泄漏「为什么」。
func TestMissingCredentialIsRejected(t *testing.T) {
	p := newIDP(t)
	v := agent.NewVerifier(nil)
	v.SetRules([]model.AccessRule{jwtRule(p)})

	code := callVerify(t, v, "api.example.com", "")
	if code == http.StatusOK {
		t.Fatal("受保护域名缺少凭据应被拒")
	}
	if code := callVerify(t, v, "api.example.com", "Basic dXNlcjpwdw=="); code == http.StatusOK {
		t.Fatal("非 Bearer 凭据应被拒")
	}
}

// JWKS 里有多把密钥时，必须按 kid 挑对那一把。
//
// 这条是变异测试暴露出来的缺口：原先所有用例的 JWKS 都只有一把密钥，
// 于是「kid 对不上就回退到任意一把」这个错误实现照样全绿。
//
// 真实风险不是放行伪造 token（回退只在受信公钥里挑），而是**密钥轮换期间
// 误拒合法 token**——IdP 同时挂着新旧两把时挑错了，用户会看到随机的 401。
func TestJWKSWithMultipleKeysSelectsByKid(t *testing.T) {
	k1, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entry := func(kid string, k *rsa.PrivateKey) map[string]string {
			return map[string]string{
				"kty": "RSA", "kid": kid, "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{entry("old", k1), entry("new", k2)},
		})
	}))
	defer srv.Close()

	v := agent.NewVerifier(nil)
	v.SetRules([]model.AccessRule{{
		ID: "jwt", Type: model.RuleJWTBearer, Enabled: true,
		ApplyTo: []string{"api.example.com"},
		Spec:    model.RuleSpec{Issuer: "https://idp.test", Audience: "edge-api", JWKS: srv.URL},
	}})

	sign := func(kid string, k *rsa.PrivateKey) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": "https://idp.test", "aud": "edge-api",
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		tok.Header["kid"] = kid
		s, err := tok.SignedString(k)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	// 两把密钥签出的 token 都要能通过——轮换期间新旧都在用
	for _, c := range []struct {
		kid string
		key *rsa.PrivateKey
	}{{"old", k1}, {"new", k2}} {
		if code := callVerify(t, v, "api.example.com", "Bearer "+sign(c.kid, c.key)); code != http.StatusOK {
			t.Errorf("kid=%s 签出的 token 应通过，实际 %d", c.kid, code)
		}
	}

	// kid 对不上时必须拒绝，不能在多把密钥里随便挑一把碰运气
	if code := callVerify(t, v, "api.example.com", "Bearer "+sign("unknown-kid", k1)); code == http.StatusOK {
		t.Error("kid 不在 JWKS 里时应被拒，而不是回退到任意一把公钥")
	}
}
