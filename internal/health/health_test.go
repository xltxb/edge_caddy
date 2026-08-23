package health_test

import (
	"context"
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
}

func (a *recordingAlerter) Notify(_ context.Context, level, title, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, level+"|"+title)
}

func (a *recordingAlerter) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.sent...)
}

type fakeDNS struct {
	mu       sync.Mutex
	detached []string
	attached []string
}

func (d *fakeDNS) Detach(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
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
	waitFor(t, 3*time.Second, func() bool {
		_, attached := d.took()
		return len(attached) > 0
	})
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
	if !contains(msg, "未变动") {
		t.Fatalf("应当说清解析没被动过: %q", msg)
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
