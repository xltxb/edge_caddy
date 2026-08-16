package deploy_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/deploy"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/store"
)

// fakeTunnel 记录每个节点收到了几次下发，并按脚本决定如何回应。
type fakeTunnel struct {
	mu    sync.Mutex
	nodes []string
	sends map[string]int
	// reply 决定某个节点第 n 次收到下发时怎么回应；返回 nil 表示不回应（模拟失联）。
	// cfg 是本次下发的版本号，直接从消息里取——编排器靠它认出结果属于哪次下发。
	reply func(node string, attempt int, cfg string) *edgev1.PushResult
	orch  *deploy.Orchestrator
}

func (f *fakeTunnel) Connected() []string { return f.nodes }

func (f *fakeTunnel) Send(node string, msg *edgev1.MasterMsg) error {
	f.mu.Lock()
	f.sends[node]++
	n := f.sends[node]
	f.mu.Unlock()

	if res := f.reply(node, n, msg.GetPush().GetCfgVersion()); res != nil {
		// 真实链路里结果是异步回来的，这里也异步，避免测试无意中依赖同步时序
		go func() {
			time.Sleep(5 * time.Millisecond)
			f.orch.OnPushResult(node, res)
		}()
	}
	return nil
}

func (f *fakeTunnel) count(node string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends[node]
}

func newOrch(t *testing.T, tun *fakeTunnel, opts ...deploy.RetryPolicy) (*deploy.Orchestrator, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "d.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.PutRoute(context.Background(), model.Route{
		Domain: "api.example.com", Upstream: "10.0.0.1:80",
		Block: model.BlockAbort, BodyMax: "1MB",
	}); err != nil {
		t.Fatal(err)
	}
	p := deploy.RetryPolicy{Max: 3, Backoff: time.Millisecond}
	if len(opts) > 0 {
		p = opts[0]
	}
	o := deploy.NewWithRetry(st, tun, p, nil)
	tun.orch = o
	return o, st
}

func okResult(cfg string) *edgev1.PushResult {
	return &edgev1.PushResult{CfgVersion: cfg, Ok: true, Detail: "31ms"}
}

// 节点没有回应（超时 / 断连）属于传输层失败，要重试。
func TestRetriesWhenNodeNeverAnswers(t *testing.T) {
	tun := &fakeTunnel{nodes: []string{"node-a"}, sends: map[string]int{}}
	// 前两次不回应，第三次回成功
	tun.reply = func(_ string, attempt int, cfg string) *edgev1.PushResult {
		if attempt < 3 {
			return nil
		}
		return okResult(cfg)
	}
	o, _ := newOrch(t, tun, deploy.RetryPolicy{Max: 3, Backoff: time.Millisecond, Deadline: 80 * time.Millisecond})

	res, err := o.Deploy(context.Background(), "abiu")
	if err != nil {
		t.Fatal(err)
	}
	if tun.count("node-a") != 3 {
		t.Fatalf("未回应应重试到第 3 次，实际发送 %d 次", tun.count("node-a"))
	}
	if res.Rows[0].State != "ok" {
		t.Fatalf("第 3 次成功后整体应为 ok，实际 %+v", res.Rows[0])
	}
}

// Caddy 明确拒绝了配置 —— **不重试**。
//
// 同一份字节喂给同一个 Caddy 必然得到同一个拒绝，重试只会在日志里刷 N 遍
// 一模一样的报错，把真正的原因埋进噪声里。能修它的是人改配置，不是时间。
// 这是 ADR-0005 定的规则，也是最容易被后人「修成统一重试」的地方。
func TestDoesNotRetryWhenCaddyRejectsConfig(t *testing.T) {
	tun := &fakeTunnel{nodes: []string{"node-a"}, sends: map[string]int{}}
	tun.reply = func(_ string, _ int, cfg string) *edgev1.PushResult {
		return &edgev1.PushResult{
			CfgVersion: cfg,
			Ok:         false,
			Detail:     "caddy 拒绝 apps/http（HTTP 500）: unknown handler nope_xyz",
		}
	}
	o, _ := newOrch(t, tun)

	res, err := o.Deploy(context.Background(), "abiu")
	if err != nil {
		t.Fatal(err)
	}
	if n := tun.count("node-a"); n != 1 {
		t.Fatalf("配置被拒绝时不应重试，实际发送 %d 次", n)
	}
	if res.Rows[0].State != "fail" {
		t.Fatalf("应记为失败，实际 %+v", res.Rows[0])
	}
	// 原文必须原样带到结果里——排查时唯一有用的就是它
	if !contains(res.Rows[0].Detail, "unknown handler nope_xyz") {
		t.Fatalf("失败原文应原样保留，实际 %q", res.Rows[0].Detail)
	}
}

// 重试到上限仍未回应时记为失败，且原因要说清是没回应，不是配置错。
func TestGivesUpAfterMaxAttempts(t *testing.T) {
	tun := &fakeTunnel{nodes: []string{"node-a"}, sends: map[string]int{}}
	tun.reply = func(string, int, string) *edgev1.PushResult { return nil }
	o, _ := newOrch(t, tun, deploy.RetryPolicy{Max: 2, Backoff: time.Millisecond, Deadline: 40 * time.Millisecond})

	res, err := o.Deploy(context.Background(), "abiu")
	if err != nil {
		t.Fatal(err)
	}
	if n := tun.count("node-a"); n != 2 {
		t.Fatalf("应尝试 2 次后放弃，实际 %d 次", n)
	}
	if res.Rows[0].State != "fail" || !contains(res.Rows[0].Detail, "deadline") {
		t.Fatalf("超时失败的原因应能与配置错误区分开，实际 %+v", res.Rows[0])
	}
}

// 一成一败：成功的节点不受失败节点影响，两者各自记录。
func TestPartialFailureRecordsBothOutcomes(t *testing.T) {
	tun := &fakeTunnel{nodes: []string{"node-ok", "node-bad"}, sends: map[string]int{}}
	tun.reply = func(node string, _ int, cfg string) *edgev1.PushResult {
		if node == "node-ok" {
			return okResult(cfg)
		}
		return &edgev1.PushResult{CfgVersion: cfg, Ok: false, Detail: "unknown handler"}
	}
	o, st := newOrch(t, tun)

	res, err := o.Deploy(context.Background(), "abiu")
	if err != nil {
		t.Fatal(err)
	}
	byNode := map[string]string{}
	for _, r := range res.Rows {
		byNode[r.NodeID] = r.State
	}
	if byNode["node-ok"] != "ok" || byNode["node-bad"] != "fail" {
		t.Fatalf("一成一败应各自记录，实际 %v", byNode)
	}

	ds, rows, err := st.ListDeploys(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].OKCount != 1 || ds[0].FailCount != 1 {
		t.Fatalf("下发记录的计数应为 ok=1 fail=1，实际 ok=%d fail=%d", ds[0].OKCount, ds[0].FailCount)
	}
	if len(rows[ds[0].ID]) != 2 {
		t.Fatalf("应有 2 条逐节点结果，实际 %d", len(rows[ds[0].ID]))
	}
}

// 下发成功后，成功节点涉及的资源版本要 +1。
//
// 版本号是「这条路由已经在节点上生效过」的唯一凭据；不推进的话，
// 界面上永远显示「未下发」，用户会反复推同一份配置。
func TestSuccessfulDeployBumpsResourceVersion(t *testing.T) {
	tun := &fakeTunnel{nodes: []string{"node-a"}, sends: map[string]int{}}
	tun.reply = func(_ string, _ int, cfg string) *edgev1.PushResult { return okResult(cfg) }
	o, st := newOrch(t, tun)

	before, _ := st.GetRoute(context.Background(), "api.example.com")
	if _, err := o.Deploy(context.Background(), "abiu"); err != nil {
		t.Fatal(err)
	}
	after, _ := st.GetRoute(context.Background(), "api.example.com")
	if after.Version != before.Version+1 {
		t.Fatalf("下发成功后版本应 +1，实际 %d → %d", before.Version, after.Version)
	}
}

// 全部节点都失败时，资源版本**不**推进。
//
// 推进了就等于宣称「已经生效」，而实际上没有任何节点拿到它。
func TestFailedDeployDoesNotBumpVersion(t *testing.T) {
	tun := &fakeTunnel{nodes: []string{"node-a"}, sends: map[string]int{}}
	tun.reply = func(_ string, _ int, cfg string) *edgev1.PushResult {
		return &edgev1.PushResult{CfgVersion: cfg, Ok: false, Detail: "unknown handler"}
	}
	o, st := newOrch(t, tun)

	before, _ := st.GetRoute(context.Background(), "api.example.com")
	if _, err := o.Deploy(context.Background(), "abiu"); err != nil {
		t.Fatal(err)
	}
	after, _ := st.GetRoute(context.Background(), "api.example.com")
	if after.Version != before.Version {
		t.Fatalf("无一节点成功时版本不应推进，实际 %d → %d", before.Version, after.Version)
	}
}

// ── 辅助 ──

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
