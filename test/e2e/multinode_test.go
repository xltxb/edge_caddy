package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// 两个真节点，其中一个的 Caddy 是坏的：一成一败，且失败那条带着可读原因。
//
// 假隧道能覆盖重试的分支逻辑，但覆盖不了「真实失败长什么样」——节点回报的
// detail 是不是人能看懂的、成功那条会不会被失败那条拖累、失败之后修好能不能
// 重推成功。这三件事只有真链路能回答。
func TestPartialFailureAndRecoveryOnRealLink(t *testing.T) {
	caddyBin := findCaddy(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "UPSTREAM host=%s", r.Host)
	}))
	defer upstream.Close()

	edgePortA, adminPortA := freePort(t), freePort(t)
	// B 的 admin 端口上**什么都不跑**——模拟本机 Caddy 挂了
	adminPortB := freePort(t)

	startCaddy(t, caddyBin, t.TempDir(), adminPortA)

	m := startMaster(t, render.Options{Listen: []string{fmt.Sprintf("127.0.0.1:%d", edgePortA)}})

	// 订阅实时通道，稍后断言进度帧真的发出来了
	frames := m.hub.Subscribe()
	defer m.hub.Unsubscribe(frames)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	joinAgent(t, ctx, m, "node-ok-01", adminPortA)
	joinAgent(t, ctx, m, "node-bad-01", adminPortB)

	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 2 }) {
		t.Fatalf("两个节点都应接入，实际 %v", m.tun.Connected())
	}

	if err := m.st.PutRoute(ctx, model.Route{
		Domain: "multi.example.com", Upstream: hostPort(upstream.URL),
		Block: model.BlockAbort, BodyMax: "1MB",
		Whitelist: []string{model.AllowAllCIDR},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := m.orch.Deploy(ctx, "abiu")
	if err != nil {
		t.Fatalf("下发不应整体失败——有节点成功就该记录下来: %v", err)
	}

	byNode := map[string]model.DeployResult{}
	for _, r := range res.Rows {
		byNode[r.NodeID] = r
	}
	if byNode["node-ok-01"].State != "ok" {
		t.Errorf("健康节点应成功，实际 %+v", byNode["node-ok-01"])
	}
	if byNode["node-bad-01"].State != "fail" {
		t.Errorf("Caddy 不可用的节点应失败，实际 %+v", byNode["node-bad-01"])
	}
	// 失败原因要能看懂，且能与「节点没回应」区分开
	if d := byNode["node-bad-01"].Detail; d == "" || d == "deadline exceeded" {
		t.Errorf("失败原因应是可读的连接错误而非超时，实际 %q", d)
	} else {
		t.Logf("失败原因: %s", d)
	}

	// 成功那半必须真的生效，不受失败节点拖累
	if !waitFor(5*time.Second, func() bool {
		code, body := getVia(edgePortA, "multi.example.com")
		return code == http.StatusOK && len(body) >= 8 && body[:8] == "UPSTREAM"
	}) {
		code, body := getVia(edgePortA, "multi.example.com")
		t.Fatalf("健康节点的流量应已通：HTTP %d，体 %q", code, body)
	}

	// 逐节点进度帧必须发出来——PRD §7 不允许「整体成功/失败」的黑盒
	seen := drainProgress(frames, 2*time.Second)
	for _, node := range []string{"node-ok-01", "node-bad-01"} {
		if _, hit := seen[node]; !hit {
			t.Errorf("应收到 %s 的下发进度帧，实际收到 %v", node, keysOf(seen))
		}
	}

	// 修好 B 的 Caddy 后重推，它应当转为成功
	startCaddy(t, caddyBin, t.TempDir(), adminPortB)
	res2, err := m.orch.Deploy(ctx, "abiu")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res2.Rows {
		if r.State != "ok" {
			t.Errorf("修好后重推应全部成功，%s 仍失败: %s", r.NodeID, r.Detail)
		}
	}

	// 全部成功后资源版本应已推进两次（第一次一成一败也算有成功）
	route, err := m.st.GetRoute(ctx, "multi.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if route.Version != 2 {
		t.Errorf("两次下发都有节点成功，版本应为 2，实际 %d", route.Version)
	}
}

// joinAgent 让一个真 Agent 接入并跑起来。
func joinAgent(t *testing.T, ctx context.Context, m *master, nodeID string, caddyAdminPort int) {
	t.Helper()
	tok, _, err := m.enroller.Issue(ctx, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cfg := agent.Config{
		NodeID: nodeID, MasterAddr: m.addr, ServerName: "master.local",
		StateDir: t.TempDir(), MasterCA: m.ca.RootPEM(),
		CaddyAdmin:        fmt.Sprintf("http://127.0.0.1:%d", caddyAdminPort),
		HeartbeatInterval: 100 * time.Millisecond,
	}
	if err := agent.Enroll(ctx, cfg, tok); err != nil {
		t.Fatalf("%s 接入失败: %v", nodeID, err)
	}
	go func() { _ = agent.Run(ctx, cfg) }()
}

// drainProgress 在给定时间内收集下发进度帧，返回出现过的节点集合。
func drainProgress(ch chan ws.Frame, d time.Duration) map[string]struct{} {
	seen := map[string]struct{}{}
	deadline := time.After(d)
	for {
		select {
		case f := <-ch:
			if f.Type == "deploy_progress" {
				if node, ok := f.Data["node"].(string); ok {
					seen[node] = struct{}{}
				}
			}
		case <-deadline:
			return seen
		}
	}
}

func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
