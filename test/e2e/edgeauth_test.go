package e2e

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
)

// 真 Caddy + 真 Agent 校验端点 + 真 JWKS：合法 token 放行并透传声明，
// 伪造签名被拒，未受保护域名不受影响。
//
// 这条是 #7 的全部意义所在。单测能证明验签逻辑对，但证明不了 Caddy 真的会把
// 请求委托过去、真的会按状态码放行/拒绝、声明真的能透传到源站——那三件事
// 只有真链路能回答。
func TestEdgeAuthOnRealCaddy(t *testing.T) {
	caddyBin := findCaddy(t)

	// 源站把收到的已验证声明回显出来，便于断言透传
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "UPSTREAM sub=%s", r.Header.Get("X-Verified-Sub"))
	}))
	defer upstream.Close()

	// 真 JWKS
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer idpSrv.Close()

	sign := func(k *rsa.PrivateKey, sub string) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": "https://idp.test", "aud": "edge-api", "sub": sub,
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		tok.Header["kid"] = "k1"
		s, err := tok.SignedString(k)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	rules := []model.AccessRule{{
		ID: "app-jwt", Type: model.RuleJWTBearer, Enabled: true,
		ApplyTo: []string{"secure.example.com"},
		Spec: model.RuleSpec{
			Issuer: "https://idp.test", Audience: "edge-api", JWKS: idpSrv.URL, SkewSec: 60,
		},
	}}

	// Agent 的校验端点
	verifyPort := freePort(t)
	verifyAddr := fmt.Sprintf("127.0.0.1:%d", verifyPort)
	v := agent.NewVerifier(nil)
	v.SetRules(rules)
	srv, err := v.Serve(verifyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// 真 Caddy，配置由真渲染器产出
	edgePort, adminPort := freePort(t), freePort(t)
	startCaddy(t, caddyBin, t.TempDir(), adminPort)

	routes := []model.Route{
		{Domain: "secure.example.com", Upstream: hostPort(upstream.URL),
			Block: model.BlockAbort, BodyMax: "1MB", Whitelist: []string{model.AllowAllCIDR}},
		{Domain: "open.example.com", Upstream: hostPort(upstream.URL),
			Block: model.BlockAbort, BodyMax: "1MB", Whitelist: []string{model.AllowAllCIDR}},
	}
	blob, err := render.CaddyWith(routes, render.Options{
		Listen:     []string{fmt.Sprintf("127.0.0.1:%d", edgePort)},
		Rules:      rules,
		VerifyAddr: verifyAddr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.NewCaddyClient(fmt.Sprintf("http://127.0.0.1:%d", adminPort)).
		Apply(t.Context(), blob); err != nil {
		t.Fatalf("下发配置失败: %v", err)
	}

	get := func(host, authz string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", edgePort), nil)
		req.Host = host
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		buf := make([]byte, 256)
		n, _ := resp.Body.Read(buf)
		return resp.StatusCode, string(buf[:n])
	}

	// 合法 token：放行，且**已验证的主体透传到了源站**
	code, body := get("secure.example.com", "Bearer "+sign(key, "user-42"))
	if code != http.StatusOK {
		t.Fatalf("合法 token 应放行，实际 %d（%s）", code, body)
	}
	if body != "UPSTREAM sub=user-42" {
		t.Fatalf("已验证的主体应透传给源站，实际响应体 %q", body)
	}

	// 用别的密钥签的 token：被拒
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	if code, _ := get("secure.example.com", "Bearer "+sign(other, "attacker")); code == http.StatusOK {
		t.Error("伪造签名的 token 必须被拒")
	}
	// 结构像 JWT 但瞎编的串：被拒。上一版的格式检查会放行它。
	if code, _ := get("secure.example.com", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"); code == http.StatusOK {
		t.Error("瞎编签名的 token 必须被拒")
	}
	// 无凭据：被拒
	if code, _ := get("secure.example.com", ""); code == http.StatusOK {
		t.Error("受保护域名缺少凭据应被拒")
	}

	// 未受保护的域名：不带凭据也照常通
	if code, _ := get("open.example.com", ""); code != http.StatusOK {
		t.Errorf("未受保护的域名不应受鉴权影响，实际 %d", code)
	}

	// 校验端点挂掉后 fail-closed：受保护域名不可用，但**不会被绕过**
	_ = srv.Close()
	if code, _ := get("secure.example.com", "Bearer "+sign(key, "user-42")); code == http.StatusOK {
		t.Error("校验端点不可用时不得放行——必须 fail-closed")
	}
	if code, _ := get("open.example.com", ""); code != http.StatusOK {
		t.Error("校验端点挂掉不应影响未受保护的域名")
	}
}
