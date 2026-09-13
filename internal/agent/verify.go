package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xltxb/edge_caddy/internal/model"
)

// VerifyServer 是 Agent 在回环地址上暴露的**校验端点**。
//
// **这条委托是 ADR-0003 的决定**（docs/adr/0003-edge-auth-via-agent-forward-auth.md）。
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

	seen  *replayCache
	limit *limiter
	geo   *geoDB

	// rateLimited 是被限流拦下的请求数，累计。
	//
	// **限流这一半不能从 Caddy 的指标里读**：429 是校验端点回的，
	// 经 reverse_proxy 透传，而 reverse_proxy 也处理正常回源 ——
	// 分不出「我们限的」和「上游自己回的 429」。
	// 这里数得精确：只有真的拒过才加。
	rateLimited atomic.Uint64
	denied      atomic.Uint64
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

	Requests  int
	WindowSec int
	RateKey   string

	GeoMode      string
	GeoCountries []string
}

func NewVerifyServer(log *slog.Logger) *VerifyServer {
	if log == nil {
		log = slog.Default()
	}
	return &VerifyServer{
		log:   log,
		rules: map[string]*verifyRule{},
		seen:  newReplayCache(),
		limit: newLimiter(),
		geo:   newGeoDB(),
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
			Skew:     time.Duration(r.SkewSec) * time.Second,
			Requests: r.Requests, WindowSec: r.WindowSec, RateKey: r.RateKey,
			GeoMode: r.GeoMode, GeoCountries: r.GeoCountries,
		}
	}
	v.mu.Lock()
	v.rules = m
	v.mu.Unlock()
}

// RunSweeper 定期清掉已经攒满的桶。
//
// **不跑它的话那张表会随攻击一起长大** —— 一个僵尸网络每个 IP 打一次，
// 每个 IP 留下一个桶，而那正是限流要防的那种流量。
// 一个能被它要防的攻击撑爆的防护，是放大器不是防线。
//
// 写完 sweep 之后我一度没有把它接到任何地方 —— 这个仓库里数到第七次的
// 同一个形状：**机制建好了，没接到会跑它的那个循环上**。
func (v *VerifyServer) RunSweeper(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			v.mu.RLock()
			rules := make(map[string]*verifyRule, len(v.rules))
			for k, r := range v.rules {
				rules[k] = r
			}
			v.mu.RUnlock()
			if n := v.limit.sweepAll(rules); n > 0 {
				v.log.Debug("清理限流桶", "清掉", n, "剩余", v.limit.size())
			}
			// **重放缓存也在这里清。** admit 不再扫表之后（issue #74），
			// 一条再也不会被问到的过期签名没有任何别的路径会删掉它——
			// 不接上这一行，那张表就只增不减，比原先更糟。
			if n := v.seen.sweep(); n > 0 {
				v.log.Debug("清理重放缓存", "清掉", n, "剩余", v.seen.size())
			}
		}
	}
}

func (v *VerifyServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/verify/", v.countDenied(http.HandlerFunc(v.handleVerify)))
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

	// **限流走单独一条路，因为它的拒绝语义不同。**
	//
	// 403 说的是「你不该来」，429 说的是「你来得太快了，等等再来」——
	// 而客户端（尤其是自动重试的那些）会按这个区别决定要不要重试。
	// 混成一个的话，一次限流会被当成鉴权失败，重试逻辑直接放弃。
	if rule.Type == "rate_limit" {
		key := rateKeyOf(clientIPOf(r), r.Header.Get("X-Forwarded-Uri"), rule.RateKey)
		burst := rule.Requests
		rate := 0.0
		if rule.WindowSec > 0 {
			rate = float64(rule.Requests) / float64(rule.WindowSec)
		}
		if ok, wait := v.limit.allow(rule.ID+"|"+key, burst, rate); !ok {
			v.rateLimited.Add(1)
			w.Header().Set("Retry-After", itoaSec(wait))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("X-Verified-Rule", ruleID)
		w.WriteHeader(http.StatusOK)
		return
	}

	if rule.Type == "geo_block" {
		ip := clientIPOf(r)
		country, err := v.geo.Country(ip)
		if errors.Is(err, ErrNoGeoDB) {
			// **放行，并且说出来。**
			//
			// 拒绝的话，库还没下发到的那段时间里每个受地域规则保护的域名
			// 对所有人都是 403，而配置看起来完全正常。
			// 把「我们没准备好」变成所有访问者的 403，
			// 是把一次运维疏忽放大成一次全站故障。
			//
			// 代价是**库没到之前这条规则形同虚设** —— 所以它必须被看见：
			// 这条日志会进 Agent 的日志缓冲，节点页上看得到。
			v.log.Warn("地域规则暂时不生效：本机还没有 GeoIP 库",
				"rule", ruleID)
			w.Header().Set("X-Verified-Rule", ruleID)
			w.WriteHeader(http.StatusOK)
			return
		}
		if !geoAllows(country, rule.GeoMode, rule.GeoCountries) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Verified-Rule", ruleID)
		w.WriteHeader(http.StatusOK)
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
	mu sync.Mutex
	// seen 记的是**过期时刻**，不是写入时刻。
	//
	// 记写入时刻的话，清理就得知道「这条是哪个规则的、窗口多长」——
	// 而缓存是所有规则共享的，此前拿当前调用的 ttl 一刀切：短窗口规则的
	// 每次请求都会把长窗口规则还在窗口内的签名清掉，重放保护的实际强度
	// 变成取决于同一台节点上邻居规则怎么配（issue #27）。反方向同样错：
	// 邻居窗口长时，过期的条目也清不掉。每条记自己的死期，
	// 清理就不需要知道任何规则的存在。
	seen map[string]time.Time

	// scans 是累计扫过的条目数，只为让「admit 不扫表」这件事可被断言。
	// 生产路径上没人读它（与 commitFault / RetryBackoff 同一个惯例）。
	scans int

	// now 可注入，测试里不必真的等。与 limiter 同一个惯例。
	now func() time.Time
}

func newReplayCache() *replayCache {
	return &replayCache{seen: map[string]time.Time{}, now: time.Now}
}

// admit 返回 true 表示这个签名此前没出现过。
// admit 是**热路径**：只查、只插，不扫表。
//
// 这里原先顺手遍历整张表清过期条目。表的规模是「窗口内的合法签名数」——
// 高 QPS 的受保护域名上，每个请求都是一次 O(n) 的串行扫描，而且握着锁
// （issue #74）。限流桶那边早就是另一条路：清理交给 RunSweeper 定期做。
//
// **过期判断仍然在这里做**，只是只判被问到的那一条：一条过了窗口的签名
// 要重新放行，否则它永远用不了第二次——而那件事不能等到下一次 sweep。
func (c *replayCache) admit(key string, ttl time.Duration) bool {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.scans++ // 只为测试能问「刚才扫了几条」，生产路径上没人读它
	if dies, seen := c.seen[key]; seen && !now.After(dies) {
		return false
	}
	c.seen[key] = now.Add(ttl)
	return true
}

// sweep 清掉已经过期的条目，由 RunSweeper 定期调。
//
// **不跑它的话那张表只增不减**：admit 不再扫表之后，一条再也不会被问到的
// 过期签名就没有任何路径会删掉它。与限流桶的 sweepAll 是同一件事、同一个
// 理由——「一个能被它要防的攻击撑爆的防护，是放大器不是防线」。
func (c *replayCache) sweep() int {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k, dies := range c.seen {
		c.scans++
		if now.After(dies) {
			delete(c.seen, k)
			n++
		}
	}
	return n
}

// size / scanned 供测试与日志。scanned 是**累计扫过的条目数**——
// 判据取它而不是耗时：耗时看机器快慢，扫描量是确定的。
func (c *replayCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}

func (c *replayCache) scanned() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scans
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

// normalizeAddr 把一个校验端点地址收成可比的形式。
//
// **它真的要做归一化。** 这个函数曾经只是 strings.TrimSpace，而注释承诺的
// 两件事一件都没做——于是 EC_VERIFY_LISTEN 写成 `:2020` 或 `0.0.0.0:2020`
// （与主控渲染的 `127.0.0.1:2020` 实际等价、连得通），CheckVerifyAddr 照样
// 整份拒绝下发，并给出一条「请让两边一致」的错误，而人已经认为它们一致了
// （issue #63）。
//
// 归一化只做两件事，都是**同一台机器上的同一个端点**的不同写法：
//
//   - unix socket：路径 Clean 一下。它不与任何 tcp 地址相等。
//   - host:port：空 host、`0.0.0.0`、`::`、`localhost` 一律收成回环——
//     它们在「Caddy 连得到本机这个端口吗」这个问题上是同一个答案。
//     别的 host（比如 192.168.1.9）**不收**：那可能真是另一台机器。
func normalizeAddr(a string) string {
	a = strings.TrimSpace(a)
	if p, ok := strings.CutPrefix(a, "unix/"); ok {
		return "unix/" + path.Clean(p)
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		// 认不出来就原样比。宁可报一次「两边不一致」，
		// 也不要把两个真不同的地址归一成同一个。
		return a
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]", "localhost":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// itoaSec 把等待时长写成 Retry-After 要的整秒数。
func itoaSec(d time.Duration) string {
	n := int(d / time.Second)
	if n < 1 {
		n = 1
	}
	return strconv.Itoa(n)
}

// clientIPOf 是**这一侧唯一一处**决定「客户端 IP 从哪个头读」的地方。
//
// 限流与地域都要它，而两处各读一遍的话，哪天换了头名字会只改一处 ——
// 症状是其中一种规则按另一个来源计数，而两者平时给出同一个值，
// 于是它在测试里和平时都看不出来。
func clientIPOf(r *http.Request) string {
	return r.Header.Get("X-Edge-Client-IP")
}

// VerifyKinds 是这个校验端点认得的规则类型。
//
// **它是从 verify 那个 switch 里长出来的，不是另写一份清单。**
// 另写的话，加第八种规则时改了 switch 忘了清单 —— 而那时主控会以为
// 节点支持它，照常下发，然后那个域名的第一个请求就是 403。
//
// 加新类型时**两处都要改**，而漏改会被 TestVerifyKindsCoversEveryHandledType 抓住。
func VerifyKinds() []string {
	return []string{"service_secret", "jwt_bearer", "rate_limit", "geo_block"}
}

// LoadGeoDB 换上一份 GeoIP 库。空路径卸载。
func (v *VerifyServer) LoadGeoDB(path string) error { return v.geo.Load(path) }

// RateLimited 是被限流拦下的请求数，累计（进程重启归零）。
func (v *VerifyServer) RateLimited() uint64 { return v.rateLimited.Load() }

// Denied 是校验端点拒掉的请求数，累计（进程重启归零）。**限流的也算在内**：
// 它们同样没有到源站，而这个数存在的理由就是把「到了源站」和「没到」分开。
//
// 主控那边用它校正回源率：在 forward_auth 处被拒的请求也记在
// handler="reverse_proxy" 上，而 Caddy 的计数器分不出它和真正的回源
// （两者渲染出来是同一个 handler）。这一侧分得出（issue #41）。
func (v *VerifyServer) Denied() uint64 { return v.denied.Load() }

// countDenied 在**一处**数拒绝，而不是在六个出口各加一行。
//
// 加在出口上的话，下一个人加第七个出口时不会想起这件事——而漏掉的症状是
// 回源率悄悄偏高，没有任何东西会说出来。
func (v *VerifyServer) countDenied(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status < 200 || rec.status > 299 {
			v.denied.Add(1)
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
