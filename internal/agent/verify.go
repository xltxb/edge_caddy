package agent

import (
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/xltxb/edge_caddy/internal/model"
)

// Verifier 是 Agent 在回环上暴露的鉴权端点。
//
// Caddy 通过 forward_auth 把受保护域名的请求先交给它（docs/adr/0003）：
// 官方 Caddy 既没有 JWT 模块也没有 HMAC 模块，真正的验签只能在这里做。
// 200 放行，其余状态码拒绝。
//
// **多条规则之间是「或」**：满足任意一条即放行。这对应 PRD §5 的「双轨准入
// ——第三方系统走 IP 白名单 + 请求签名，终端客户端走 JWT，两条通道互不影响」。
// 注意这条语义的含义：给一个域名**加**一条规则是在**增加**一条进入的路径，
// 而不是在收紧。想收紧就该改已有规则，而不是再加一条。
type Verifier struct {
	mu    sync.RWMutex
	rules []model.AccessRule
	jwks  sync.Map // url -> *jwksEntry
	log   *slog.Logger
}

func NewVerifier(log *slog.Logger) *Verifier {
	if log == nil {
		log = slog.Default()
	}
	return &Verifier{log: log}
}

// SetRules 替换当前规则集，由配置下发时调用。
func (v *Verifier) SetRules(rules []model.AccessRule) {
	v.mu.Lock()
	v.rules = rules
	v.mu.Unlock()
}

func (v *Verifier) rulesFor(host string) []model.AccessRule {
	v.mu.RLock()
	defer v.mu.RUnlock()
	// 去掉端口：Host 头可能带端口，而规则绑定的是域名
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	out := make([]model.AccessRule, 0, 2)
	for _, r := range v.rules {
		if !r.Enabled {
			continue // 停用必须真的不生效
		}
		for _, d := range r.ApplyTo {
			if d == host {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

func (v *Verifier) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rules := v.rulesFor(r.Host)
	if len(rules) == 0 {
		// 未受保护的域名直接放行：它们不该因为鉴权而变慢或变脆
		w.WriteHeader(http.StatusOK)
		return
	}

	var lastErr error
	for _, rule := range rules {
		claims, err := v.check(rule, r)
		if err == nil {
			// 把已验证的声明透传给源站，源站不必重新解析 token
			for k, val := range claims {
				w.Header().Set(k, val)
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		lastErr = err
	}

	// 对外只说「未通过」，不说是哪一条规则、哪一步失败——
	// 区分会告诉试探者他离通过还差什么。详细原因留给日志。
	v.log.Warn("边缘鉴权未通过", "host", r.Host, "err", lastErr)
	w.WriteHeader(http.StatusUnauthorized)
}

func (v *Verifier) check(rule model.AccessRule, r *http.Request) (map[string]string, error) {
	switch rule.Type {
	case model.RuleJWTBearer:
		return v.checkJWT(rule, r)
	case model.RuleServiceSecret:
		return v.checkSecret(rule, r)
	case model.RuleIPWhitelist:
		return nil, checkIP(rule, r)
	default:
		return nil, fmt.Errorf("未知的规则类型 %q", rule.Type)
	}
}

// ── JWT ──

func (v *Verifier) checkJWT(rule model.AccessRule, r *http.Request) (map[string]string, error) {
	raw, ok := bearerToken(r)
	if !ok {
		return nil, fmt.Errorf("缺少 Bearer 凭据")
	}
	keys, err := v.fetchJWKS(rule.Spec.JWKS)
	if err != nil {
		return nil, fmt.Errorf("获取 JWKS: %w", err)
	}

	skew := time.Duration(rule.Spec.SkewSec) * time.Second
	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512"}),
		jwt.WithLeeway(skew),
	}
	if rule.Spec.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(rule.Spec.Issuer))
	}
	if rule.Spec.Audience != "" {
		opts = append(opts, jwt.WithAudience(rule.Spec.Audience))
	}

	tok, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if k, ok := keys[kid]; ok {
			return k, nil
		}
		// 没有 kid 或对不上时，只有唯一一把密钥才回退——多把时回退等于
		// 允许攻击者挑一把去碰运气。
		if len(keys) == 1 {
			for _, k := range keys {
				return k, nil
			}
		}
		return nil, fmt.Errorf("找不到 kid %q 对应的公钥", kid)
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("校验 JWT: %w", err)
	}

	out := map[string]string{}
	if mc, ok := tok.Claims.(jwt.MapClaims); ok {
		if sub, _ := mc["sub"].(string); sub != "" {
			out["X-Verified-Sub"] = sub
		}
		if iss, _ := mc["iss"].(string); iss != "" {
			out["X-Verified-Issuer"] = iss
		}
	}
	return out, nil
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) <= len(p) || !strings.EqualFold(h[:len(p)], p) {
		return "", false
	}
	return h[len(p):], true
}

type jwksEntry struct {
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

// jwksTTL 是 JWKS 的缓存时长。
//
// 必须缓存：每个请求都去 IdP 取会把 IdP 打挂，也让边缘的可用性绑在它身上。
// 取 10 分钟而不是更长，是为了让密钥轮换能在可接受的时间内自动生效。
const jwksTTL = 10 * time.Minute

func (v *Verifier) fetchJWKS(url string) (map[string]*rsa.PublicKey, error) {
	if url == "" {
		return nil, fmt.Errorf("规则未配置 JWKS 地址")
	}
	if e, ok := v.jwks.Load(url); ok {
		entry := e.(*jwksEntry)
		if time.Since(entry.fetchedAt) < jwksTTL {
			return entry.keys, nil
		}
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	blob, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Keys []struct {
			Kty, Kid, N, E string
		} `json:"keys"`
	}
	if err := json.Unmarshal(blob, &doc); err != nil {
		return nil, fmt.Errorf("解析 JWKS: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(n),
			E: int(new(big.Int).SetBytes(e).Int64()),
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("JWKS 里没有可用的 RSA 公钥")
	}
	v.jwks.Store(url, &jwksEntry{keys: keys, fetchedAt: time.Now()})
	return keys, nil
}

// ── 服务密钥（HMAC + 重放窗口）──

func (v *Verifier) checkSecret(rule model.AccessRule, r *http.Request) (map[string]string, error) {
	header := rule.Spec.Header
	if header == "" {
		header = "X-Service-Secret"
	}
	sig := r.Header.Get(header)
	if sig == "" {
		return nil, fmt.Errorf("缺少 %s", header)
	}
	tsRaw := r.Header.Get(header + "-Timestamp")
	if tsRaw == "" {
		return nil, fmt.Errorf("缺少时间戳")
	}
	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("时间戳格式不对")
	}
	// 重放窗口：签名本身是可重放的，只有把时间戳纳入签名并限制窗口，
	// 截获到一个请求的人才不能无限期地重放它。
	ttl := time.Duration(rule.Spec.TTLSec) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if d := time.Since(time.Unix(ts, 0)); d > ttl || d < -ttl {
		return nil, fmt.Errorf("时间戳超出重放窗口")
	}

	mac := hmac.New(sha256.New, []byte(rule.Spec.Secret))
	fmt.Fprintf(mac, "%s\n%s\n%s\n%d", r.Method, r.Host, r.URL.Path, ts)
	want := hex.EncodeToString(mac.Sum(nil))
	// 恒定时间比较：普通 == 会在首个不同字节处提前返回，泄漏「前缀猜对了多少」
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return nil, fmt.Errorf("签名不匹配")
	}
	return map[string]string{"X-Verified-Client": rule.ID}, nil
}

// ── IP 白名单 ──

func checkIP(rule model.AccessRule, r *http.Request) error {
	ip := clientIP(r)
	for _, cidr := range rule.Spec.IPs {
		if matchIP(ip, cidr) {
			return nil
		}
	}
	return fmt.Errorf("来源 %s 不在白名单内", ip)
}

// clientIP 取请求的真实来源。
//
// forward_auth 让 Caddy 转发请求给这个端点，因此 RemoteAddr 是 Caddy 自己。
// 真实来源在 X-Forwarded-For 的**第一段**——但只在请求确实来自本机 Caddy 时
// 才可信。这个端点只监听回环，外部无法直接访问，所以这里信任该头是安全的。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// matchIP 判断一个 IP 是否落在给定的 IP 或 CIDR 内。
func matchIP(ip, pattern string) bool {
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	if strings.Contains(pattern, "/") {
		_, netw, err := net.ParseCIDR(pattern)
		return err == nil && netw.Contains(addr)
	}
	return net.ParseIP(pattern).Equal(addr)
}

// Serve 在回环上起校验端点。
//
// **只监听回环**：这个端点回什么状态码就决定了请求放不放行，暴露到外网等于
// 把鉴权决定权交出去。它也因此可以信任 X-Forwarded-For——只有本机 Caddy
// 够得着它。
func (v *Verifier) Serve(addr string) (*http.Server, error) {
	if !strings.HasPrefix(addr, "127.0.0.1:") && !strings.HasPrefix(addr, "[::1]:") {
		return nil, fmt.Errorf("校验端点只能监听回环地址，实际 %q", addr)
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("校验端点监听 %s: %w", addr, err)
	}
	srv := &http.Server{Handler: v, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(lis) }()
	v.log.Info("校验端点已启动", "addr", addr)
	return srv, nil
}

// UpstreamCertPath / UpstreamKeyPath 是回源客户端证书在节点上的落盘位置。
// 与 render 包里的常量必须一致——Caddy 按那个路径去读。
var (
	UpstreamCertPath = "/etc/edge-agent/pki/upstream.crt"
	UpstreamKeyPath  = "/etc/edge-agent/pki/upstream.key"
)

// writeUpstreamCert 把主控签发的回源证书落盘。
//
// 私钥 0600：同机其他用户读到它就能冒充这台节点去连源站。
func writeUpstreamCert(cert, key []byte) error {
	if err := os.MkdirAll(filepath.Dir(UpstreamCertPath), 0o700); err != nil {
		return fmt.Errorf("创建证书目录: %w", err)
	}
	if err := os.WriteFile(UpstreamCertPath, cert, 0o600); err != nil {
		return fmt.Errorf("写入回源证书: %w", err)
	}
	if err := os.WriteFile(UpstreamKeyPath, key, 0o600); err != nil {
		return fmt.Errorf("写入回源私钥: %w", err)
	}
	return nil
}
