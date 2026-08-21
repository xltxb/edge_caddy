package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// verifyJWT 校验 Bearer token 并返回它的 sub。
//
// JWKS 在 Agent 内**缓存**：这条路径出现在每一个受保护请求上，
// 每次去 IdP 取会把 IdP 变成边缘的同步依赖，而且延迟会加在每个请求上
// （ADR-0003）。
func (v *VerifyServer) verifyJWT(rule *verifyRule, r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(auth) <= len(p) || !strings.EqualFold(auth[:len(p)], p) {
		return "", fmt.Errorf("缺少 Bearer token")
	}
	raw := strings.TrimSpace(auth[len(p):])

	opts := []jwt.ParserOption{
		jwt.WithLeeway(rule.Skew),
		jwt.WithExpirationRequired(),
	}
	if rule.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(rule.Issuer))
	}
	if rule.Audience != "" {
		opts = append(opts, jwt.WithAudience(rule.Audience))
	}

	tok, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return jwksCacheFor(rule.JWKSURL).key(kid, t.Method.Alg())
	}, opts...)
	if err != nil {
		return "", fmt.Errorf("token 无效: %w", err)
	}

	sub, err := tok.Claims.GetSubject()
	if err != nil {
		return "", nil // 没有 sub 不算失败，只是没什么可透传的
	}
	return sub, nil
}

// ---- JWKS 缓存 ----

const jwksTTL = 10 * time.Minute

var (
	jwksMu     sync.Mutex
	jwksCaches = map[string]*jwksCache{}
)

func jwksCacheFor(url string) *jwksCache {
	jwksMu.Lock()
	defer jwksMu.Unlock()
	c := jwksCaches[url]
	if c == nil {
		c = &jwksCache{url: url}
		jwksCaches[url] = c
	}
	return c
}

type jwksCache struct {
	url string

	mu        sync.Mutex
	keys      map[string]any
	fetchedAt time.Time
}

func (c *jwksCache) key(kid, alg string) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.keys == nil || time.Since(c.fetchedAt) > jwksTTL {
		if err := c.refreshLocked(); err != nil {
			// 取不到就整体拒绝，不放行。fail-closed 与 ADR-0003 的姿态一致：
			// IdP 不可达时受保护域名应当拒绝，而不是敞开。
			return nil, fmt.Errorf("拉取 JWKS: %w", err)
		}
	}
	if k, ok := c.keys[kid]; ok {
		return k, nil
	}
	// 没有 kid 且只有一把键时用它——不少 IdP 的单键 JWKS 不带 kid。
	if kid == "" && len(c.keys) == 1 {
		for _, k := range c.keys {
			return k, nil
		}
	}
	return nil, fmt.Errorf("JWKS 里没有 kid=%q（alg=%s）", kid, alg)
}

func (c *jwksCache) refreshLocked() error {
	cli := &http.Client{Timeout: 5 * time.Second}
	resp, err := cli.Get(c.url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Crv string `json:"crv"`
			N   string `json:"n"`
			E   string `json:"e"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return err
	}

	keys := map[string]any{}
	for _, k := range doc.Keys {
		switch k.Kty {
		case "RSA":
			n, err := b64(k.N)
			if err != nil {
				continue
			}
			e, err := b64(k.E)
			if err != nil {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{
				N: new(big.Int).SetBytes(n),
				E: int(new(big.Int).SetBytes(e).Int64()),
			}
		case "EC":
			x, err := b64(k.X)
			if err != nil {
				continue
			}
			y, err := b64(k.Y)
			if err != nil {
				continue
			}
			var curve elliptic.Curve
			switch k.Crv {
			case "P-256":
				curve = elliptic.P256()
			case "P-384":
				curve = elliptic.P384()
			default:
				continue
			}
			keys[k.Kid] = &ecdsa.PublicKey{
				Curve: curve,
				X:     new(big.Int).SetBytes(x),
				Y:     new(big.Int).SetBytes(y),
			}
		}
	}
	if len(keys) == 0 {
		return fmt.Errorf("JWKS 里没有可用的键")
	}
	c.keys, c.fetchedAt = keys, time.Now()
	return nil
}

func b64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
