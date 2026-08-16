package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

// 探活是一次**真的往返**：主控发出去、节点回来，时延才有意义。
//
// 上一版这里只测到「消息进了发送队列」，那个数字恒等于几微秒，跟节点是死是活
// 毫无关系——一台断网但 TCP 还没超时的节点，它照样返回 0ms。
// 这条测试跑真 master + 真 Agent + 真 Caddy，验的就是回报确实来自对面。
func TestProbeIsARealRoundTrip(t *testing.T) {
	caddyBin := findCaddy(t)
	edgePort, adminPort := freePort(t), freePort(t)
	startCaddy(t, caddyBin, t.TempDir(), adminPort)

	m := startMaster(t, render.Options{Listen: []string{fmt.Sprintf("127.0.0.1:%d", edgePort)}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	joinAgent(t, ctx, m, "node-probe-01", adminPort)

	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 1 }) {
		t.Fatal("Agent 未能接入")
	}

	rep, err := m.tun.Probe(ctx, "node-probe-01", 3*time.Second)
	if err != nil {
		t.Fatalf("探活失败: %v", err)
	}
	// 往返必须真花了时间。恒为 0 说明它根本没等对面回话。
	if rep.RTT <= 0 {
		t.Errorf("往返时延应为正数，实际 %v", rep.RTT)
	}
	if rep.RTT > 3*time.Second {
		t.Errorf("往返时延不该超过超时上限，实际 %v", rep.RTT)
	}
	// 节点侧真去问了本机 Caddy：Admin 可达
	if !rep.CaddyOK {
		t.Errorf("节点上的 Caddy 是活的，应报可达，实际 detail=%q", rep.CaddyDetail)
	}
	t.Logf("往返 %v，Caddy: %s", rep.RTT, rep.CaddyDetail)
}

// 节点上的 Caddy 挂了时，探活本身仍要成功，并如实报告 Caddy 不可达。
//
// 把两者混成一个「探活失败」会让人跑错方向：一个是隧道断了（去查网络），
// 一个是 Caddy 挂了（去那台机器上把它拉起来）。
func TestProbeReportsDeadCaddySeparatelyFromDeadTunnel(t *testing.T) {
	findCaddy(t) // 只为跳过没有 Caddy 的环境，本用例故意不启动它
	edgePort := freePort(t)
	deadAdminPort := freePort(t) // 这个端口上什么都不跑

	m := startMaster(t, render.Options{Listen: []string{fmt.Sprintf("127.0.0.1:%d", edgePort)}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	joinAgent(t, ctx, m, "node-nocaddy-01", deadAdminPort)

	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 1 }) {
		t.Fatal("Agent 未能接入")
	}

	rep, err := m.tun.Probe(ctx, "node-nocaddy-01", 3*time.Second)
	if err != nil {
		t.Fatalf("隧道是通的，探活不该失败: %v", err)
	}
	if rep.CaddyOK {
		t.Error("节点上没有 Caddy 在跑，不该报可达")
	}
	if rep.CaddyDetail == "" {
		t.Error("Caddy 不可达时应给出原因，好让人知道去哪查")
	}
	t.Logf("Caddy 不可达原因: %s", rep.CaddyDetail)
}

// 探活带回节点当前生效的配置版本与最近日志——节点行展开要显示这三样。
func TestProbeCarriesEffectiveConfigAndRecentLogs(t *testing.T) {
	caddyBin := findCaddy(t)
	edgePort, adminPort := freePort(t), freePort(t)
	startCaddy(t, caddyBin, t.TempDir(), adminPort)

	m := startMaster(t, render.Options{Listen: []string{fmt.Sprintf("127.0.0.1:%d", edgePort)}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	joinAgent(t, ctx, m, "node-logs-01", adminPort)

	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 1 }) {
		t.Fatal("Agent 未能接入")
	}

	if err := m.st.PutRoute(ctx, model.Route{
		Domain: "probe.example.com", Upstream: "127.0.0.1:1",
		Block: model.BlockAbort, BodyMax: "1MB", Whitelist: []string{model.AllowAllCIDR},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := m.orch.Deploy(ctx, "abiu", nil)
	if err != nil {
		t.Fatal(err)
	}

	rep, err := m.tun.Probe(ctx, "node-logs-01", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// 生效配置版本来自节点自己，不是主控以为的那个——两者不一致正是漂移
	if rep.CfgVersion != res.CfgVersion {
		t.Errorf("节点生效版本应为 %s，实际 %q", res.CfgVersion, rep.CfgVersion)
	}
	if len(rep.Logs) == 0 {
		t.Fatal("应带回 Agent 最近日志")
	}
	// 刚下发过，日志里应能看到这次下发
	joined := strings.Join(rep.Logs, "\n")
	if !strings.Contains(joined, res.CfgVersion) {
		t.Errorf("最近日志应包含刚下发的版本 %s，实际:\n%s", res.CfgVersion, joined)
	}
}

// 未连接的节点探活返回 ErrNodeNotConnected，且不会挂在那里等超时。
func TestProbeOnDisconnectedNodeFailsFast(t *testing.T) {
	edgePort := freePort(t)
	m := startMaster(t, render.Options{Listen: []string{fmt.Sprintf("127.0.0.1:%d", edgePort)}})

	start := time.Now()
	_, err := m.tun.Probe(context.Background(), "node-ghost", 3*time.Second)
	if err == nil {
		t.Fatal("未连接的节点探活应报错")
	}
	if !isNotConnected(err) {
		t.Errorf("应是「节点未连接」，实际 %v", err)
	}
	// 立刻失败，不是等满 3 秒——否则界面会转圈转到人以为卡死
	if d := time.Since(start); d > time.Second {
		t.Errorf("未连接应立即失败，实际等了 %v", d)
	}
}

func isNotConnected(err error) bool {
	return err != nil && strings.Contains(err.Error(), tunnel.ErrNodeNotConnected.Error())
}
