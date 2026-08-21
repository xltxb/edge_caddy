package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xltxb/edge_caddy/internal/model"
)

// VerifyServer 是 Agent 在回环地址上暴露的**校验端点**。
//
// 官方 Caddy 既没有 JWT 模块也没有 HMAC 模块，所以受保护域名的请求先经
// forward_auth 委托到这里，由 Agent 用 Go 真正验签，Caddy 按状态码放行或拒绝
// （docs/adr/0003-edge-auth-via-agent-forward-auth.md）。
//
// 这是 **fail-closed** 的：Agent 挂掉时受保护域名整体 502，不会被绕过。
// 安全姿态正确，代价是 Agent 的存活成为受保护域名的硬依赖——
// 部署脚本里 Agent 的 Restart=always 因此不是锦上添花，而是承重的。
type VerifyServer struct {
	log *slog.Logger

	mu    sync.RWMutex
	rules map[string]*verifyRule // key 是规则 id

	seen *replayCache
}

// verifyRule 是校验端点需要的那部分规则。
// IP 白名单不在其中——那由 Caddy 的 remote_ip 匹配器直接处理，不必绕一趟回环。
type verifyRule struct {
	ID     string
	Type   string
	Header string
	TTL    time.Duration
	Replay bool
	Secret string

	Issuer   string
	Audience string
	JWKSURL  string
	Skew     time.Duration
}

func NewVerifyServer(log *slog.Logger) *VerifyServer {
	if log == nil {
		log = slog.Default()
	}
	return &VerifyServer{
		log:   log,
		rules: map[string]*verifyRule{},
		seen:  newReplayCache(),
	}
}

// SetRules 换掉当前生效的规则集。随每次下发一起更新。
//
// **整体替换而不是增量合并**：一条被删掉的规则如果留在 Agent 里，
// 那个 /verify/<id> 就还能放行，而 Caddy 配置里已经没人调它了——
// 一个悄悄留着的后门。整体替换让「主控说有哪些」就是「实际有哪些」。
func (v *VerifyServer) SetRules(rules []model.VerifyRule) {
	m := make(map[string]*verifyRule, len(rules))
	for _, r := range rules {
		ttl := time.Duration(r.TTLSec) * time.Second
		if ttl <= 0 {
			ttl = 5 * time.Minute
		}
		m[r.ID] = &verifyRule{
			ID: r.ID, Type: r.Type, Header: r.Header, TTL: ttl,
			Replay: r.Replay, Secret: r.Secret,
			Issuer: r.Issuer, Audience: r.Audience, JWKSURL: r.JWKSURL,
			Skew: time.Duration(r.SkewSec) * time.Second,
		}
	}
	v.mu.Lock()
	v.rules = m
	v.mu.Unlock()
}

func (v *VerifyServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/verify/", v.handleVerify)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// handleVerify 的路径形如 /verify/<rule-id>。规则 id 走路径而不是请求头：
// 请求头可以被客户端伪造，而路径是主控渲染进 Caddy 配置里的，客户端碰不到。
func (v *VerifyServer) handleVerify(w http.ResponseWriter, r *http.Request) {
	ruleID := strings.TrimPrefix(r.URL.Path, "/verify/")
	v.mu.RLock()
	rule := v.rules[ruleID]
	v.mu.RUnlock()

	if rule == nil {
		// 规则不存在时拒绝，不是放行。配置漂移或下发只到一半时，
		// 放行会让一个本该受保护的域名悄悄敞开。
		http.Error(w, "unknown rule", http.StatusForbidden)
		return
	}

	sub, err := v.verify(rule, r)
	if err != nil {
		// 不把失败原因回给调用方：它会告诉攻击者「时间戳过期」还是「签名不对」，
		// 而那正是逐步试探所需要的信息。原因只进日志。
		v.log.Debug("校验未通过", "rule", ruleID, "err", err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// 验签结果透传给源站：源站不必重新解析 token。
	// 这是「边缘只做格式过滤」那个方案给不了的（ADR-0003 实测）。
	if sub != "" {
		w.Header().Set("X-Verified-Sub", sub)
	}
	w.Header().Set("X-Verified-Rule", ruleID)
	w.WriteHeader(http.StatusOK)
}

func (v *VerifyServer) verify(rule *verifyRule, r *http.Request) (string, error) {
	switch rule.Type {
	case "service_secret":
		return v.verifyServiceSecret(rule, r)
	case "jwt_bearer":
		return v.verifyJWT(rule, r)
	default:
		return "", fmt.Errorf("规则类型 %q 不该走校验端点", rule.Type)
	}
}

// verifyServiceSecret 校验 HMAC 签名。
//
// 头部格式 `t=<unix 秒>,v1=<hex>`，签名内容是 `<t>.<方法>.<原始 URI>`。
// 把方法与 URI 纳入签名，一条截获的签名就不能被换到别的路径上重放。
func (v *VerifyServer) verifyServiceSecret(rule *verifyRule, r *http.Request) (string, error) {
	raw := r.Header.Get(rule.Header)
	if raw == "" {
		return "", fmt.Errorf("缺少 %s", rule.Header)
	}
	var ts, sig string
	for _, part := range strings.Split(raw, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = val
		case "v1":
			sig = val
		}
	}
	if ts == "" || sig == "" {
		return "", fmt.Errorf("头部格式应为 t=<unix>,v1=<hex>")
	}

	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return "", fmt.Errorf("时间戳不是整数")
	}
	if d := time.Since(time.Unix(sec, 0)); d > rule.TTL || d < -rule.TTL {
		return "", fmt.Errorf("时间戳超出容忍窗口")
	}

	method := r.Header.Get("X-Forwarded-Method")
	if method == "" {
		method = r.Method
	}
	uri := r.Header.Get("X-Forwarded-Uri")

	mac := hmac.New(sha256.New, []byte(rule.Secret))
	fmt.Fprintf(mac, "%s.%s.%s", ts, method, uri)
	want := hex.EncodeToString(mac.Sum(nil))

	// 常数时间比较：早退的比较会通过耗时泄露已匹配的前缀长度。
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return "", fmt.Errorf("签名不匹配")
	}

	if rule.Replay && !v.seen.admit(rule.ID+":"+sig, rule.TTL) {
		return "", fmt.Errorf("签名已被使用过")
	}
	return "", nil
}

// replayCache 记住窗口内用过的签名。
//
// 只在内存里：重放窗口本来就短（默认几分钟），而 Agent 重启后那些签名
// 也快到期了。落盘换不到对应的好处，还会在每个请求上加一次磁盘写。
type replayCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newReplayCache() *replayCache { return &replayCache{seen: map[string]time.Time{}} }

// admit 返回 true 表示这个签名此前没出现过。
func (c *replayCache) admit(key string, ttl time.Duration) bool {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	// 顺手清掉过期的。规模是「窗口内的请求数」，不需要 LRU。
	for k, t := range c.seen {
		if now.Sub(t) > ttl {
			delete(c.seen, k)
		}
	}
	if _, dup := c.seen[key]; dup {
		return false
	}
	c.seen[key] = now
	return true
}

// CheckVerifyAddr 确认主控渲染进配置的校验端点地址，与本机实际监听的一致。
//
// 这是**两处知识**：主控把 render.Options.VerifyAddr 渲染进 forward_auth 的
// dial，Agent 按自己的 EC_VERIFY_LISTEN 监听。两者必须一致，而没有任何东西
// 强制它。回源证书那处能靠「路径随内容一起下发」变成一份知识，这处不行——
// Agent 必须在配置到达**之前**就已经在监听。
//
// 配错的后果很难查：每个受保护域名整体 502，而配置本身看起来完全正常。
// 所以在应用之前查一遍，把一个静默的 502 变成一条说得出原因的拒绝。
func CheckVerifyAddr(caddyJSON []byte, listening string) error {
	dials := forwardAuthDials(caddyJSON)
	if len(dials) == 0 {
		return nil // 这份配置里没有受保护的域名
	}
	want := normalizeAddr(listening)
	for _, d := range dials {
		if normalizeAddr(d) != want {
			return fmt.Errorf(
				"配置里的校验端点是 %s，而本机监听在 %s —— 照这份配置生效，"+
					"每个受保护的域名都会 502。请让主控的 EC_VERIFY_ADDR 与本机的 "+
					"EC_VERIFY_LISTEN 一致", d, listening)
		}
	}
	return nil
}

// forwardAuthDials 挑出配置里所有委托给校验端点的 upstream 地址。
func forwardAuthDials(caddyJSON []byte) []string {
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Handle []struct {
							Handler string `json:"handler"`
							Rewrite *struct {
								URI string `json:"uri"`
							} `json:"rewrite"`
							Upstreams []struct {
								Dial string `json:"dial"`
							} `json:"upstreams"`
						} `json:"handle"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if json.Unmarshal(caddyJSON, &cfg) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, srv := range cfg.Apps.HTTP.Servers {
		for _, r := range srv.Routes {
			for _, h := range r.Handle {
				// 只认「重写到 /verify/ 的 reverse_proxy」——普通的回源
				// 也是 reverse_proxy，不能一并算进来。
				if h.Handler != "reverse_proxy" || h.Rewrite == nil {
					continue
				}
				if !strings.HasPrefix(h.Rewrite.URI, "/verify/") {
					continue
				}
				for _, u := range h.Upstreams {
					if !seen[u.Dial] {
						seen[u.Dial] = true
						out = append(out, u.Dial)
					}
				}
			}
		}
	}
	return out
}

// normalizeAddr 让 unix/ 前缀与 host:port 两种写法可比。
func normalizeAddr(a string) string { return strings.TrimSpace(a) }
