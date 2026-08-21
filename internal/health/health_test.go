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
