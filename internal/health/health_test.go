package health_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/health"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

type recordingAlerter struct {
	mu   sync.Mutex
	sent []string
	// bodies 是告警正文 —— **人真正读到的那段话**。
	//
	// 它此前被丢掉了，于是这个包里没有任何一条测试断言过它，
	// 而这份代码里关于「措辞必须与实际发生的事一致」的注释有好几处。
	// **被反复叮嘱的那件事，恰恰一条测试都没有。**
	bodies []string
}

func (a *recordingAlerter) Notify(_ context.Context, level, title, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, level+"|"+title)
	a.bodies = append(a.bodies, body)
}

func (a *recordingAlerter) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.sent...)
}

func (a *recordingAlerter) allBodies() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.bodies...)
}

type fakeDNS struct {
	mu       sync.Mutex
	detached []string
	attached []string
	// detachErr 让测试造出「服务商配好了，而这次摘不掉」这一档。
	detachErr error
}

func (d *fakeDNS) Detach(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.detachErr != nil {
		return d.detachErr
	}
	d.detached = append(d.detached, id)
	return nil
}

func (d *fakeDNS) Attach(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attached = append(d.attached, id)
	return nil
}

func (d *fakeDNS) took() ([]string, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.detached...), append([]string(nil), d.attached...)
}

func newMonitor(t *testing.T, a health.Alerter, d health.DNSDetacher) (*health.Monitor, *store.Store) {
	t.Helper()
	st := testdb.New(t)
	if err := st.UpsertNode(context.Background(), store.NodeSpec{
		NodeID: "node-a", City: "香港", Vendor: "v", Line: "l", PublicIP: "203.0.113.7",
	}); err != nil {
		t.Fatal(err)
	}
	return health.New(health.Config{
		Store: st, Alert: a, DNS: d,
		Interval: 10 * time.Millisecond, Threshold: 3,
	}), st
}

func hb(cpu float64) tunnel.Heartbeat {
	return tunnel.Heartbeat{NodeID: "node-a", CPU: cpu, CfgVersion: "cfg-1"}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// 连续错过 N 个周期才判离线，而且只判一次。
//
// 判定用「连续错过」而不是「距上次心跳超过 N×周期」：后者在主控刚启动、
// 还没收到任何心跳时会把全部节点判成离线，而它们可能一直好好的。
func TestNodeGoesDownAfterThresholdMisses(t *testing.T) {
	a := &recordingAlerter{}
	d := &fakeDNS{}
	m, st := newMonitor(t, a, d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	m.Observe(hb(12))
	waitFor(t, 3*time.Second, func() bool { return len(a.all()) > 0 })

	// 只告警一次，不会每个周期刷一条。
	time.Sleep(120 * time.Millisecond)
	if got := a.all(); len(got) != 1 || got[0] != "crit|节点离线 node-a" {
		t.Fatalf("告警 = %v，想要恰好一条 crit", got)
	}

	var status string
	var dnsEnabled bool
	if err := st.Pool.QueryRow(ctx,
		`SELECT status::text, dns_enabled FROM edge_nodes WHERE id='node-a'`).
		Scan(&status, &dnsEnabled); err != nil {
		t.Fatal(err)
	}
	if status != "down" || dnsEnabled {
		t.Fatalf("status=%s dns_enabled=%v，想要 down/false", status, dnsEnabled)
	}

	detached, _ := d.took()
	if len(detached) != 1 || detached[0] != "node-a" {
		t.Fatalf("应当摘除解析，实际 %v", detached)
	}
}

// 恢复由首个新心跳驱动。
func TestNodeRecoversOnNextHeartbeat(t *testing.T) {
	a := &recordingAlerter{}
	d := &fakeDNS{}
	m, _ := newMonitor(t, a, d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	m.Observe(hb(12))
	waitFor(t, 3*time.Second, func() bool { return len(a.all()) > 0 })

	m.Observe(hb(15)) // 心跳回来了

	// **等告警，不等 DNS.Attach。** recover() 的顺序是「写库 → 推服务商 → 发告警」，
	// 等前面任何一步都会在告警还没发的时候往下走。
	// 同一个坑在 TestAutoDetachedDNSComesBackOnRecovery 里实测过：20 次红 2 次。
	waitFor(t, 3*time.Second, func() bool { return len(a.all()) >= 2 })
	if _, attached := d.took(); len(attached) == 0 {
		t.Fatal("恢复时应当把解析推回服务商")
	}
	if got := a.all(); len(got) < 2 || got[1] != "warn|节点恢复 node-a" {
		t.Fatalf("应当发一条恢复告警，实际 %v", got)
	}
}

// **没有配 DNS 服务商时，事件文案不能说「已暂停解析」。**
//
// 那是承诺一件没发生的事，和「ok 不等于已生效」是同一类问题。
func TestEventDoesNotClaimDNSChangedWhenNoProvider(t *testing.T) {
	a := &recordingAlerter{}
	m, st := newMonitor(t, a, nil) // 没有 DNS 实现

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	m.Observe(hb(12))
	waitFor(t, 3*time.Second, func() bool { return len(a.all()) > 0 })

	events, err := st.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("应当写事件")
	}
	msg := events[0].Msg
	if contains(msg, "已暂停 DNS 解析") {
		t.Fatalf("没有配置服务商时不该声称解析被暂停了: %q", msg)
	}
	// **措辞从「未变动」改成了「仍指向这台机器」。**
	//
	// 前者说的是「什么没发生」，后者说的是「后果是什么」——而人被半夜叫醒时
	// 需要知道的是后者：流量还在往一台死机器上打。
	//
	// 这条断言原先钉的是那个词，不是它要守的性质。钉词的代价是：
	// 措辞一改进它就红，而红的不是行为。所以改成钉性质：
	// **说清解析没被摘掉，并且说出为什么**。
	if !contains(msg, "仍指向这台机器") {
		t.Fatalf("应当说清解析没被摘掉: %q", msg)
	}
	if !contains(msg, "未配置服务商") {
		t.Fatalf("应当说出为什么没摘掉 —— 原因不止一种，而它们指向不同的动作: %q", msg)
	}
}

// sparkline 没有数据时返回 nil，让上层给出 JSON null 而不是一串 0。
// 0 会被读成「负载为零」。
func TestCPUSeriesIsNilBeforeAnyHeartbeat(t *testing.T) {
	m, _ := newMonitor(t, nil, nil)
	if got := m.CPUSeries("node-a"); got != nil {
		t.Fatalf("还没有心跳时应当是 nil，实际 %v", got)
	}
	m.Observe(hb(20))
	if got := m.CPUSeries("node-a"); len(got) != 1 || got[0] != 20 {
		t.Fatalf("收到心跳后 = %v，想要 [20]", got)
	}
}

// 环形缓冲只保留最近 12 点，最新在末尾。
func TestCPUSeriesKeepsLastTwelvePoints(t *testing.T) {
	m, _ := newMonitor(t, nil, nil)
	for i := 1; i <= 20; i++ {
		m.Observe(hb(float64(i)))
	}
	got := m.CPUSeries("node-a")
	if len(got) != 12 {
		t.Fatalf("长度 = %d，想要 12", len(got))
	}
	if got[0] != 9 || got[11] != 20 {
		t.Fatalf("窗口 = %v，想要 9..20（最新在末尾）", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// warn 是「连着但不健康」：高负载的心跳判 warn，而不是 ok。
//
// 把这样一台机器算进「在线」，KPI 会在一台 CPU 81%、内存快满的机器上仍然
// 显示绿色——而巡检时最该被看见的恰恰是那台。
func TestHighLoadHeartbeatClassifiesAsWarn(t *testing.T) {
	a := &recordingAlerter{}
	m, st := newMonitor(t, a, nil)
	ctx := context.Background()

	if got := m.Observe(tunnel.Heartbeat{NodeID: "node-a", CPU: 12, CfgVersion: "cfg-1"}); got != "ok" {
		t.Fatalf("低负载应当是 ok，实际 %q", got)
	}
	if got := m.Observe(tunnel.Heartbeat{NodeID: "node-a", CPU: 81, CfgVersion: "cfg-1"}); got != "warn" {
		t.Fatalf("CPU 81%% 应当是 warn，实际 %q", got)
	}
	if got := m.Observe(tunnel.Heartbeat{NodeID: "node-a", Mem: 95, CfgVersion: "cfg-1"}); got != "warn" {
		t.Fatalf("内存 95%% 应当是 warn，实际 %q", got)
	}

	// 状态变化时写事件并告警，且只在**变化**时——
	// 一台持续高负载的机器会把事件流刷满，而那条流的价值在于「有事发生了」。
	waitFor(t, 2*time.Second, func() bool { return len(a.all()) > 0 })
	before := len(a.all())
	for i := 0; i < 5; i++ {
		m.Observe(tunnel.Heartbeat{NodeID: "node-a", CPU: 85, CfgVersion: "cfg-1"})
	}
	time.Sleep(120 * time.Millisecond)
	if got := len(a.all()); got != before {
		t.Fatalf("持续高负载不该反复告警：%d → %d", before, got)
	}

	events, err := st.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var sawWarn bool
	for _, e := range events {
		if e.Kind == "warn" && contains(e.Msg, "负载偏高") {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Fatalf("应当写一条负载偏高的事件，实际 %+v", events)
	}
}

// 负载回落时要回到 ok 并说一声，否则那台机器会永远挂着「异常」。
func TestLoadRecoveryReturnsToOK(t *testing.T) {
	a := &recordingAlerter{}
	m, _ := newMonitor(t, a, nil)

	m.Observe(tunnel.Heartbeat{NodeID: "node-a", CPU: 90, CfgVersion: "cfg-1"})
	waitFor(t, 2*time.Second, func() bool { return len(a.all()) > 0 })

	if got := m.Observe(tunnel.Heartbeat{NodeID: "node-a", CPU: 10, CfgVersion: "cfg-1"}); got != "ok" {
		t.Fatalf("负载回落后应当回到 ok，实际 %q", got)
	}
}

// **已下线的节点不该报离线告警。** 那不是故障，是我们自己关的（ADR-0014）。
//
// 下线之后节点必然停止心跳——主控断了它的隧道并拒绝它重连。所以离线判定
// 一定会命中，而它命中的是一件我们主动做的事。
//
// 这条不是「顺手做得更好」：一个下线之后仍然每分钟报警的系统，会教会运维
// 忽略那一类告警，而下次真有机器挂了的时候他们照旧会忽略。
func TestDrainedNodeDoesNotRaiseOfflineAlert(t *testing.T) {
	a := &recordingAlerter{}
	d := &fakeDNS{}
	m, st := newMonitor(t, a, d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := st.SetNodeDrained(ctx, "node-a", true, "abiu"); err != nil {
		t.Fatal(err)
	}
	go m.Run(ctx)

	m.Observe(hb(12))
	// 等足够久：不下线的话这段时间里 TestNodeGoesDownAfterThresholdMisses
	// 早就收到告警了。
	time.Sleep(600 * time.Millisecond)

	if got := a.all(); len(got) != 0 {
		t.Fatalf("已下线的节点不该报告警，实际 %v", got)
	}
	detached, _ := d.took()
	if len(detached) != 0 {
		t.Errorf("已下线的节点解析早就摘了，不该再摘一次：%v", detached)
	}
}

// **主控重启之后，一台此时已经失联的节点必须仍然被判离线。**
//
// 灰度上撞到的：`node-hk-01` 31 分钟没心跳，控制台显示「在线」。
//
// 根因是 m.nodes 只在**收到心跳时**才建条目。主控重启后 map 清空，
// 而一台已经死了的机器再也不会发心跳，就永远进不了这张 map——
// tick 遍历不到它，SetNodeDown 一次也不会被调用，库里的 status
// 永久停在 ok。
//
// 连带的两件事更贵：SetNodeDown 同时负责**摘解析**和**发离线告警**。
// 它没跑，那台死机器就还挂在解析里，而且没有人被通知。
// （灰度上没造成实害，只是因为 DNS 服务商还没配上。）
//
// **而制造这个洞的，正是上面那条测试注释里的那个理由。**
// 「数连续错过的次数，而不是看距上次心跳多久」——顾虑是对的，
// 但「数次数」只对已经在 map 里的节点数。
// **一道防误报的措施，造出了一个永久的漏报。**
//
// 这条测试模拟的正是那个场景：库里有节点、Monitor 是全新的（没收过任何心跳）。
func TestNodeUnseenSinceRestartStillGoesDown(t *testing.T) {
	a := &recordingAlerter{}
	d := &fakeDNS{}
	m, st := newMonitor(t, a, d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// **一次 Observe 都不调**——这就是「主控重启后，那台机器再也没来过」。
	go m.Run(ctx)

	waitFor(t, 3*time.Second, func() bool { return len(a.all()) > 0 })

	var status string
	var dnsEnabled bool
	if err := st.Pool.QueryRow(ctx,
		`SELECT status::text, dns_enabled FROM edge_nodes WHERE id='node-a'`).
		Scan(&status, &dnsEnabled); err != nil {
		t.Fatal(err)
	}
	if status != "down" {
		t.Errorf("status=%s，想要 down —— 一台主控从没见过心跳的节点"+
			"不能永远停在上一次的状态", status)
	}
	if dnsEnabled {
		t.Error("还挂在解析里 —— 流量会往一台死机器上打")
	}
	if got := a.all(); len(got) != 1 || got[0] != "crit|节点离线 node-a" {
		t.Errorf("告警 = %v，想要恰好一条 crit", got)
	}
}

// **库里已经是 down 的，重启之后不该再报一次警。**
//
// 不挡的话，每一次主控重启都会为同一台死机器重新走一遍 markDown：
// 再发一次告警、再摘一次解析。**一个重启就重复报警的系统，
// 会教会运维忽略那一类告警**——跟「被人下线的节点不报离线」是同一条理由。
func TestAlreadyDownNodeDoesNotReAlertAfterRestart(t *testing.T) {
	a := &recordingAlerter{}
	d := &fakeDNS{}
	m, st := newMonitor(t, a, d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := st.SetNodeDown(ctx, "node-a"); err != nil {
		t.Fatal(err)
	}

	go m.Run(ctx)
	time.Sleep(150 * time.Millisecond) // 足够跑好几轮 tick

	if got := a.all(); len(got) != 0 {
		t.Errorf("库里已经是 down 了，重启不该再报警，实际 %v", got)
	}
	// **但它仍然要在内存里被看着**：下一次心跳来了要能走恢复那条路。
	//
	// 验的是恢复告警，不是库里的 status——`recover` 不写库，
	// 状态是隧道的心跳回调经 TouchHeartbeat 落的。
	// （我第一版断言了库里变成 ok，红了；红的是我的断言，不是代码。）
	m.Observe(hb(10))
	waitFor(t, time.Second, func() bool {
		for _, x := range a.all() {
			if x == "warn|节点恢复 node-a" {
				return true
			}
		}
		return false
	})
}

// **系统自己摘掉的解析，心跳恢复时要放回去。**
//
// 摘和恢复此前不对称：SetNodeDown 在 SQL 里直接把 dns_enabled 置 false，
// 而恢复那一侧只调 dnsops.Attach —— 那个函数「只负责让服务商侧跟上」，
// **不写库**。于是标志位一旦被自动摘掉就再也回不来。
//
// 后果比「一台机器掉出解析」大得多：**主控每重启一次，所有节点都会被
// 自动摘掉**（重启窗口里它们必然错过几个心跳），而重启是例行操作。
// 灰度上就是这么发生的：部署完新版本，节点 7 秒后重连回来，
// 而它已经不在解析里，且没有任何东西会把它放回去。
func TestAutoDetachedDNSComesBackOnRecovery(t *testing.T) {
	a := &recordingAlerter{}
	d := &fakeDNS{}
	m, st := newMonitor(t, a, d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	// 先让它被判离线（系统摘解析）。
	waitFor(t, 3*time.Second, func() bool { return len(a.all()) > 0 })
	var enabled bool
	var reason *string
	if err := st.Pool.QueryRow(ctx,
		`SELECT dns_enabled, dns_reason::text FROM edge_nodes WHERE id='node-a'`).
		Scan(&enabled, &reason); err != nil {
		t.Fatal(err)
	}
	if enabled || reason == nil || *reason != "auto_offline" {
		t.Fatalf("前置条件不成立：enabled=%v reason=%v", enabled, reason)
	}

	// 心跳回来。
	//
	// **等的必须是最后发生的那件事。**
	//
	// 这里原先等的是库里 dns_enabled 转 true，然后立刻断言告警发出来了——
	// 而 recover() 的顺序是「先写库，再发告警」。于是库那一步一到，
	// 测试就往下走，告警可能还没发。**它 flaky，而 flaky 的测试会在两个
	// 方向上说谎**：偶尔红一次让人以为产品坏了，而它平时的绿也不能当证据。
	//
	// 等告警就同时等到了库（库在它前面），反过来不成立。
	m.Observe(hb(10))
	waitFor(t, 3*time.Second, func() bool {
		for _, x := range a.all() {
			if strings.Contains(x, "节点恢复") {
				return true
			}
		}
		return false
	})

	// reason / actor 一起清掉：留着 auto_offline 而 dns_enabled 是 true，
	// 是一句自相矛盾的记录，而界面会照着它引导人「先去修那台机器」。
	if err := st.Pool.QueryRow(ctx,
		`SELECT dns_enabled, dns_reason::text FROM edge_nodes WHERE id='node-a'`).
		Scan(&enabled, &reason); err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Error("系统摘的解析，心跳恢复时该放回去")
	}
	if reason != nil {
		t.Errorf("恢复之后 reason 该清空，实际 %q —— "+
			"auto_offline 配上 dns_enabled=true 是一句自相矛盾的记录", *reason)
	}

	// **恢复的措辞要说出解析也回来了。** 只说「心跳已恢复」的话，
	// 人无从知道那台机器现在在不在解析里。
	var found bool
	for _, x := range a.all() {
		if strings.Contains(x, "节点恢复") {
			found = true
		}
	}
	if !found {
		t.Errorf("应当发一条恢复告警，实际 %v", a.all())
	}
}

// **人手动关掉的解析，系统不能替他开。**
//
// 没有这一条，一个「恢复时无条件置 true」的实现也能让上面那条通过 ——
// 而它会让一次心跳抖动撤销掉人的决定。
//
// 这是 ADR-0014「意图与观察分开」在这一处的具体形态：
// auto_offline 是观察驱动的，观察变了就跟着变；manual 是意图，不随观察动。
func TestManuallyPausedDNSIsNotReattachedBySystem(t *testing.T) {
	a := &recordingAlerter{}
	d := &fakeDNS{}
	m, st := newMonitor(t, a, d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 人手动关掉解析。
	if err := st.SetNodeDNS(ctx, "node-a", false, store.DNSManual, "abiu"); err != nil {
		t.Fatal(err)
	}

	go m.Run(ctx)
	waitFor(t, 3*time.Second, func() bool { return len(a.all()) > 0 }) // 判离线
	m.Observe(hb(10))                                                  // 心跳回来
	time.Sleep(200 * time.Millisecond)

	var enabled bool
	var reason, actor *string
	if err := st.Pool.QueryRow(ctx,
		`SELECT dns_enabled, dns_reason::text, dns_actor FROM edge_nodes WHERE id='node-a'`).
		Scan(&enabled, &reason, &actor); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("人手动关的解析，系统不该替他开 —— " +
			"一次心跳抖动不能撤销人的决定")
	}
	if reason == nil || *reason != store.DNSManual {
		t.Errorf("reason 该保持 manual，实际 %v —— 自动摘除覆盖了人的决定", deref(reason))
	}
	if actor == nil || *actor != "abiu" {
		t.Errorf("操作人该保持住，实际 %v", deref(actor))
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestOfflineAlertSaysWhyDNSWasNotDetached 钉的是**「摘不掉」的原因要说对**。
//
// 这里原先无论什么错都说「未配置服务商」。灰度上真实发生过：
// 服务商配好了，而库里的权重五条线不一致、纯 DNS 表达不了，
// 于是每次摘除都失败——**而告警把人送去配一个已经配好的东西**。
//
// 一句错的诊断比没有诊断更贵：它给了人一个方向，而那个方向是反的。
//
// 判据是「它说出了真正的原因」，不是「它报了错」——后者原先也满足。
func TestOfflineAlertSaysWhyDNSWasNotDetached(t *testing.T) {
	a := &recordingAlerter{}
	d := &fakeDNS{detachErr: errors.New("五条线必须配置相同的节点与权重")}
	m, _ := newMonitor(t, a, d)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	m.Observe(hb(12))
	waitFor(t, 3*time.Second, func() bool { return len(a.all()) > 0 })

	got := a.allBodies()[0]
	if strings.Contains(got, "未配置服务商") {
		t.Errorf("服务商是配好的，却说「未配置服务商」—— "+
			"这会把人送去配一个已经配好的东西：%q", got)
	}
	if !strings.Contains(got, "五条线") {
		t.Errorf("没说出真正的原因：%q", got)
	}
	if !strings.Contains(got, "仍指向这台机器") {
		t.Errorf("要说清解析没被摘掉 —— 人被叫醒时流量还在往这台死机器上打：%q", got)
	}
}

// TestWarnDoesNotStormWhenLoadHoversOnTheThreshold：抖在阈值上不该反复告警。
//
// classify 进出用的是同一个阈值，announce 只在状态**变化**时发——而「在阈值
// 上抖」正是变化最频繁的那种：CPU 在 79.8 / 80.2 之间来回（负载略高于阈值的
// 机器的常态），心跳周期 3 秒，于是每分钟最多 20 条事件 + 20 条 Lark，
// 而且 ok 与 warn 交替（issue #47）。
//
// 离线那一档有 downSent 护着，证书那一档有 alertEvery 护着。**warn 是唯一
// 没有的**，而这份代码在三处写着同一条理由：「一个重启就重复报警的系统会
// 教会人忽略那一类告警」。
//
// 判据是**人收到几条**，不是「状态对不对」——状态每一次都是对的，
// 那正是这个缺陷难被看见的原因。
func TestWarnDoesNotStormWhenLoadHoversOnTheThreshold(t *testing.T) {
	a := &recordingAlerter{}
	m, _ := newMonitor(t, a, nil)

	// 默认阈值是 80。在它上下各 0.2 来回抖十个来回。
	for i := 0; i < 10; i++ {
		m.Observe(hb(80.2))
		m.Observe(hb(79.8))
	}
	// announce 是 go 起的，给它落定的时间。
	time.Sleep(100 * time.Millisecond)

	if got := len(a.all()); got > 2 {
		t.Errorf("在阈值上抖了十个来回，发了 %d 条告警 —— "+
			"一条天天亮着的告警，人两天就学会忽略它，连带忽略真出问题那天的那条", got)
	}
}
