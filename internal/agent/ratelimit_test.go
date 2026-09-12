package agent

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestBucketAllowsBurstThenThrottles 钉的是**桶容量就是允许的突发**。
//
// 一个正常用户打开页面会并发十几个请求。按纯速率算（每秒 N 个，多一个就拦）
// 它一定会被误伤 —— 而误伤正常用户的限流，人第一次撞到就会把它关掉，
// 那时它防的东西一样进得来。
func TestBucketAllowsBurstThenThrottles(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLimiter()
	l.now = func() time.Time { return now }

	// 桶容量 10、每秒补 1（即 10 请求 / 10 秒）。
	const burst, rate = 10, 1.0

	// 头 10 个瞬间放行 —— 那正是「突发」。
	for i := 0; i < burst; i++ {
		if ok, _ := l.allow("k", burst, rate); !ok {
			t.Fatalf("第 %d 个就被拦了 —— 桶容量是 %d，突发该放行", i+1, burst)
		}
	}
	// 第 11 个拦下，并给出重试建议。
	ok, wait := l.allow("k", burst, rate)
	if ok {
		t.Fatal("桶空了还放行 —— 那样限流形同虚设")
	}
	if wait < time.Second {
		t.Errorf("Retry-After 是 %v —— 回 0 等于叫人立刻重试，"+
			"而那正是被限的那个行为", wait)
	}

	// 等 5 秒补 5 个令牌，再放行 5 个。
	now = now.Add(5 * time.Second)
	for i := 0; i < 5; i++ {
		if ok, _ := l.allow("k", burst, rate); !ok {
			t.Fatalf("补了 5 秒之后第 %d 个还被拦", i+1)
		}
	}
	if ok, _ := l.allow("k", burst, rate); ok {
		t.Error("只该补 5 个，第 6 个要拦")
	}
}

// TestBucketsAreIndependentPerKey：不同的键互不影响。
//
// 没有这一条，一个「全局一个桶」的实现也能让上面那条全绿 ——
// 而那意味着一个人被限流会连带把所有人一起限掉。
func TestBucketsAreIndependentPerKey(t *testing.T) {
	l := newLimiter()
	for i := 0; i < 3; i++ {
		l.allow("a", 3, 1)
	}
	if ok, _ := l.allow("a", 3, 1); ok {
		t.Fatal("装置坏了：a 的桶该空了")
	}
	if ok, _ := l.allow("b", 3, 1); !ok {
		t.Error("b 被 a 的限流连累了 —— 一个人触发限流不该封住所有人")
	}
}

// TestSweepDropsFullBucketsOnly 钉的是**桶表不能无限长**。
//
// 一个僵尸网络每个 IP 打一次，每个 IP 都会留下一个桶 —— 而那正是限流
// 要防的那种流量。**一个能被它要防的攻击撑爆的防护，是放大器不是防线。**
//
// 而只清「已经攒满」的：攒满意味着它此刻与不存在完全等价
// （新键的初始状态就是满的），删掉不改变任何行为。
func TestSweepDropsFullBucketsOnly(t *testing.T) {
	now := time.Unix(2000, 0)
	l := newLimiter()
	l.now = func() time.Time { return now }

	// **idle 要用掉 3 个，不是 1 个。**
	//
	// 只用 1 个的话它 1 秒就补满了，于是下面那句「1 秒时一个都不该清」
	// 是错的期望 —— 第一版就是这么写的，测试当场红，而红的是测试不是代码。
	for i := 0; i < 3; i++ {
		l.allow("r|idle", 5, 1)
	}
	for i := 0; i < 5; i++ { // 用光
		l.allow("r|busy", 5, 1)
	}
	if l.size() != 2 {
		t.Fatalf("装置坏了：该有 2 个桶，实际 %d", l.size())
	}

	// 还没到能攒满的时候，一个都不该清。
	now = now.Add(time.Second)
	if n := l.sweepPrefix("r|", 5, 1); n != 0 {
		t.Errorf("清早了 %d 个 —— 没攒满的桶删掉会把它的计数一起抹掉，"+
			"等于给正在被限的那个人重置额度", n)
	}

	// idle 只用掉 1 个，1 秒就补满了；busy 用光 5 个，要 5 秒。
	now = now.Add(4 * time.Second)
	l.sweepPrefix("r|", 5, 1)
	if l.size() != 0 {
		t.Errorf("都攒满了还留着 %d 个 —— 这张表会随攻击一起长大", l.size())
	}
}

// TestRateKeyIgnoresQueryString：ip_path 只看路径，不看查询串。
//
// 带上查询串的话，攻击者每次换一个参数就是一个新桶 ——
// **限流的键由攻击者控制，等于没有限流**，而且顺带把桶表撑爆。
func TestRateKeyIgnoresQueryString(t *testing.T) {
	a := rateKeyOf("203.0.113.7:5555", "/login?x=1", "ip_path")
	b := rateKeyOf("203.0.113.7:5555", "/login?x=2", "ip_path")
	if a != b {
		t.Errorf("换个查询参数就换了桶（%q vs %q）—— 攻击者每次换一个就绕过了", a, b)
	}
	if c := rateKeyOf("203.0.113.7:5555", "/other", "ip_path"); c == a {
		t.Error("不同路径该是不同的桶")
	}
	if ip := rateKeyOf("203.0.113.7:5555", "/login", "ip"); ip != "203.0.113.7" {
		t.Errorf("ip 模式该只按 IP 计数，实际 %q", ip)
	}
}

// TestIncompleteConfigAllows：配置不完整时放行，不是拦。
//
// 一条配了一半的限流规则不该把站点封掉。校验那一侧已经拒绝了这种配置，
// 走到这里说明是下发链路上出了别的问题 ——
// 那时「站点还能用」比「限流一定生效」重要。
func TestIncompleteConfigAllows(t *testing.T) {
	l := newLimiter()
	if ok, _ := l.allow("k", 0, 1); !ok {
		t.Error("桶容量为 0 时该放行 —— 拦的话一条配错的规则会封掉整个站点")
	}
	if ok, _ := l.allow("k", 10, 0); !ok {
		t.Error("速率为 0 时该放行")
	}
}

// TestSweepAllUsesEachRulesOwnParams 钉的是**不能拿一套参数清全部**。
//
// 桶的键带着规则 id 前缀。一条 100/60s 的规则和一条 5/60s 的规则，
// 「攒满」的门槛完全不同 —— 用宽的那套去清严的那条，
// 会把一个**还在被限的桶**当成满的删掉，等于给正在攻击的那个人重置额度。
func TestSweepAllUsesEachRulesOwnParams(t *testing.T) {
	now := time.Unix(3000, 0)
	l := newLimiter()
	l.now = func() time.Time { return now }

	rules := map[string]*verifyRule{
		"loose": {ID: "loose", Type: "rate_limit", Requests: 100, WindowSec: 1},
		"tight": {ID: "tight", Type: "rate_limit", Requests: 2, WindowSec: 60},
	}

	// loose：每秒补 100，用掉 1 个，1 秒后必然满。
	l.allow("loose|1.1.1.1", 100, 100)
	// tight：每 60 秒补 2（每秒 1/30），用光 2 个，要 60 秒才满。
	l.allow("tight|1.1.1.1", 2, 2.0/60)
	l.allow("tight|1.1.1.1", 2, 2.0/60)

	now = now.Add(2 * time.Second)
	l.sweepAll(rules)

	if l.size() != 1 {
		t.Fatalf("该只清掉 loose 那个，剩 1 个，实际剩 %d —— "+
			"清掉 tight 的桶等于给正在被限的那个人重置额度", l.size())
	}
	// 确认剩下的是 tight：它还该被限着。
	if ok, _ := l.allow("tight|1.1.1.1", 2, 2.0/60); ok {
		t.Error("tight 的桶被清掉了 —— 攻击者的额度被我们自己重置了")
	}
}

// TestSweepAllSkipsIncompleteRules：参数不全的规则不参与清理。
//
// 拿 0 当 burst 去清的话，`tokens >= 0` 恒真 —— **它会把所有桶都删掉**，
// 而那是一条配错的规则连带把整个限流器清空。
func TestSweepAllSkipsIncompleteRules(t *testing.T) {
	l := newLimiter()
	l.allow("good|1.1.1.1", 2, 1)
	l.allow("good|1.1.1.1", 2, 1)
	// bad 此前参数齐全时建过桶，之后被改成了不全 —— 桶还在。
	l.allow("bad|2.2.2.2", 2, 1)
	before := l.size()

	// 两条都在册：good 参数齐全但桶没攒满，bad 参数不全。
	// （good 必须在册 —— 不在册的桶是孤儿，会被 dropOrphans 正当清掉，
	// 那验的就不是「参数不全的规则乱清」了。）
	l.sweepAll(map[string]*verifyRule{
		"good": {ID: "good", Type: "rate_limit", Requests: 2, WindowSec: 60},
		"bad":  {ID: "bad", Type: "rate_limit", Requests: 0, WindowSec: 0},
	})
	if l.size() != before {
		t.Errorf("一条参数不全的规则清掉了不该清的桶（%d → %d）——"+
			"拿 0 当 burst 清是全删；而 bad 自己的桶也该留着：它在册，"+
			"只是此刻算不出「攒满」，清了等于重置正在被限的人的额度", before, l.size())
	}
}

// TestVerifyKindsCoversEveryHandledType 钉的是**报出去的清单与真正处理的类型一致**。
//
// 主控靠这份清单决定「这条规则能不能下发到这台节点」。**分叉的两个方向后果相反**：
//
//	报了而不处理  →  主控放行下发，而节点收到请求时走 default 回 403 —— 整站关闭
//	处理了而不报  →  主控拒绝下发，一个其实能用的功能用不了
//
// 前者是事故，后者是阻碍。而它们都**不会有任何东西变红** ——
// 除非有这么一条把两处对起来。
//
// 判法：从 verify.go 的源码里抓出 handleVerify 与 verify() 里所有
// `rule.Type == "x"` / `case "x"`，与 VerifyKinds() 逐一对账。
// **读源码而不是调用它**：调用要造出每一种规则的完整上下文，
// 而那份上下文本身就会漏掉新加的类型。
func TestVerifyKindsCoversEveryHandledType(t *testing.T) {
	src, err := os.ReadFile("verify.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	handled := map[string]bool{}

	// handleVerify 里的 `rule.Type == "x"`。
	for _, m := range regexp.MustCompile(`rule\.Type == "([a-z_]+)"`).
		FindAllStringSubmatch(text, -1) {
		handled[m[1]] = true
	}

	// verify() 里那个 switch 的 case。**只在那个函数的范围里找** ——
	// 整份文件搜 `case "x":` 会抓到别处的（HMAC 头解析里有个 `case "t":`），
	// 第一版就是那样写的，测试当场红，而红的是测试不是代码。
	if i := strings.Index(text, "func (v *VerifyServer) verify("); i >= 0 {
		body := text[i:]
		if j := strings.Index(body, "\n}\n"); j >= 0 {
			body = body[:j]
		}
		for _, m := range regexp.MustCompile(`case "([a-z_]+)":`).
			FindAllStringSubmatch(body, -1) {
			handled[m[1]] = true
		}
	}
	// 装置自检：一个都没抓到的话，下面两个循环全是空转。
	if len(handled) < 3 {
		t.Fatalf("只从 verify.go 里抓到 %d 种类型（%v）—— "+
			"写法变了？这条测试此刻什么也没检查", len(handled), handled)
	}

	reported := map[string]bool{}
	for _, k := range VerifyKinds() {
		reported[k] = true
	}

	for k := range handled {
		if !reported[k] {
			t.Errorf("verify.go 处理了 %q 而 VerifyKinds() 没报 —— "+
				"主控会拒绝下发这类规则，一个其实能用的功能用不了", k)
		}
	}
	for k := range reported {
		if !handled[k] {
			t.Errorf("VerifyKinds() 报了 %q 而 verify.go 不处理它 —— "+
				"主控会放行下发，然后节点收到请求走 default 回 403，整站关闭", k)
		}
	}
}

// TestSweepAllDropsOrphanBuckets：**已删规则的桶要清掉**。
//
// sweepAll 若只按当前规则集的前缀清，一条规则被删（或改 ID）后它留下的桶
// 不匹配任何前缀 —— 没有任何路径会清它，直到进程重启（issue #29）。
// 被 CC 打过一轮再删规则，残留的量就是攻击期间的独立 IP 数。
func TestSweepAllDropsOrphanBuckets(t *testing.T) {
	l := newLimiter()
	l.allow("deleted|1.2.3.4", 5, 1)    // 已删规则留下的桶
	l.allow("live|1.2.3.4", 5, 5.0/300) // 在册规则的桶，还远没攒满

	l.sweepAll(map[string]*verifyRule{
		"live": {ID: "live", Type: "rate_limit", Requests: 5, WindowSec: 300},
	})

	if l.size() != 1 {
		t.Fatalf("该只剩 live 的那个桶，实际剩 %d —— 孤儿桶没人会再查，留着就是泄漏", l.size())
	}
	// 剩下的必须是 live 的：它还在计数中，清掉等于重置额度。
	if ok, _ := l.allow("live|1.2.3.4", 5, 5.0/300); !ok {
		t.Error("live 的桶被误清了？（allow 拿到的是新桶，不该拒绝）")
	} else if l.size() != 1 {
		t.Error("live 的桶被清掉后又新建了一个 —— 计数丢了")
	}
}

// 换了类型的规则同理：规则还在册，但 allow 再也不会为它建桶 —— 旧桶是孤儿。
func TestSweepAllDropsBucketsOfRetypedRule(t *testing.T) {
	l := newLimiter()
	l.allow("was-rl|1.2.3.4", 5, 1)

	l.sweepAll(map[string]*verifyRule{
		"was-rl": {ID: "was-rl", Type: "ip_blacklist"}, // 从 rate_limit 改成了别的
	})
	if l.size() != 0 {
		t.Errorf("改了类型的规则的旧桶还留着 %d 个", l.size())
	}
}
