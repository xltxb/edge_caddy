package agent

import (
	"net"
	"strings"
	"sync"
	"time"
)

// bucket 是一个令牌桶。
//
// **桶容量就是「允许的突发」**：一个正常用户打开页面会并发十几个请求，
// 按纯速率算它一定会被误伤，而按桶算不会——只要它不持续这么打。
type bucket struct {
	tokens float64
	last   time.Time
}

// limiter 是**每个节点自己**的限流器。
//
// 计数不跨节点：三台节点每台限 100，全局实际是 300。
// 要全局限流就得有一个共享计数器（Redis 之类），而那给每个请求加一次
// 跨机往返——**在边缘节点上，那个往返比它要防的攻击更容易先把自己拖垮**。
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	// now 可注入，测试里不必真的等。
	now func() time.Time
}

func newLimiter() *limiter {
	return &limiter{buckets: map[string]*bucket{}, now: time.Now}
}

// allow 说这个键此刻放不放行，以及不放行时建议多久后再来。
//
// rate 是每秒补多少令牌，burst 是桶容量。
func (l *limiter) allow(key string, burst int, rate float64) (bool, time.Duration) {
	if burst <= 0 || rate <= 0 {
		// **配置不完整时放行，不是拦。**
		//
		// 一条配了一半的限流规则不该把站点封掉。校验那一侧已经拒绝了
		// 这种配置，走到这里说明是下发链路上出了别的问题——
		// 那时「站点还能用」比「限流一定生效」重要。
		return true, 0
	}

	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: float64(burst), last: now}
		l.buckets[key] = b
	} else {
		b.tokens += now.Sub(b.last).Seconds() * rate
		if b.tokens > float64(burst) {
			b.tokens = float64(burst)
		}
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	// 差多少秒才攒够一个令牌。至少回 1 秒：回 0 的话
	// Retry-After: 0 等于叫人立刻重试，而那正是被限的那个行为。
	wait := time.Duration((1-b.tokens)/rate*float64(time.Second)) + time.Second
	return false, wait.Truncate(time.Second)
}

// sweep 清掉已经攒满的桶。
//
// **不清的话这张表会无限长。** 一个僵尸网络每个 IP 打一次，
// 每个 IP 都会留下一个桶——而那正是限流要防的那种流量。
// **一个能被它要防的攻击撑爆的防护，是攻击的放大器不是防线。**
//
// 判据是「已经攒满」而不是「多久没用过」：攒满意味着它此刻与
// 不存在完全等价（新键的初始状态就是满的），删掉不改变任何行为。
// sweepAll 按每条规则各自的参数清一遍。
//
// **桶的键带着规则 id 前缀**（`<ruleID>|<ip>`），所以不能拿一套参数清全部：
// 一条 100/60s 的规则和一条 5/60s 的规则，「攒满」的门槛不一样。
// 用错参数会把一个还在被限的桶当成满的删掉 —— 等于给正在攻击的那个人重置额度。
func (l *limiter) sweepAll(rules map[string]*verifyRule) int {
	n := 0
	for _, r := range rules {
		if r.Type != "rate_limit" || r.Requests <= 0 || r.WindowSec <= 0 {
			continue
		}
		n += l.sweepPrefix(r.ID+"|", r.Requests, float64(r.Requests)/float64(r.WindowSec))
	}
	return n + l.dropOrphans(rules)
}

// dropOrphans 清掉不属于任何在册限流规则的桶。
//
// 规则被删（或改了类型）后，allow 再也不会拿它的前缀建桶或查桶 ——
// 旧桶没人会再碰，留着就只是泄漏，直到进程重启（issue #29）。
// 被 CC 打过一轮再删规则，残留的量就是攻击期间的独立 IP 数。
//
// **参数不全的规则不算孤儿的主人缺席**：它在册，只是此刻算不出「攒满」，
// 它的桶由 sweepPrefix 那一侧跳过（清早了等于重置正在被限的人的额度），
// 这里同样留着 —— 两侧对同一条规则的处置要一致。
//
// 判法是前缀匹配而不是按 "|" 切 id：规则 id 里真有 "|" 时，
// 切出来的前半段永远匹配不上，那条规则的桶会被**每分钟清一次** ——
// 等于给它的攻击者每分钟重置一次额度，而没有任何报错。
func (l *limiter) dropOrphans(rules map[string]*verifyRule) int {
	prefixes := make([]string, 0, len(rules))
	for _, r := range rules {
		if r.Type == "rate_limit" {
			prefixes = append(prefixes, r.ID+"|")
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
buckets:
	for k := range l.buckets {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				continue buckets
			}
		}
		delete(l.buckets, k)
		n++
	}
	return n
}

func (l *limiter) sweepPrefix(prefix string, burst int, rate float64) int {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, b := range l.buckets {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		if b.tokens+now.Sub(b.last).Seconds()*rate >= float64(burst) {
			delete(l.buckets, k)
			n++
		}
	}
	return n
}

func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// rateKeyOf 算出这次请求按什么计数。
//
// **客户端 IP 取自 X-Edge-Client-IP**（Caddy 用 remote.host 填的），
// 不是 X-Forwarded-For：后者客户端能伪造，读它等于让攻击者自己选计数的键。
func rateKeyOf(clientIP, uri, mode string) string {
	ip := clientIP
	if h, _, err := net.SplitHostPort(ip); err == nil {
		ip = h
	}
	if mode == "ip_path" {
		// 只取路径，丢掉查询串：带上的话攻击者每次换一个参数就是一个新桶。
		path := uri
		if i := indexByte(path, '?'); i >= 0 {
			path = path[:i]
		}
		return ip + "|" + path
	}
	return ip
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
