package deploy_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/deploy"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

// fakePusher 让「节点不回应」这件事可以被确定地制造出来。
//
// 这不违反 ADR-0011 那条「不要内存假实现」：那条针对的是**存储路径**——
// 假的仓储层会让生产方言的 SQL 从未被验证。这里库是真的，真隧道也已经被
// e2e 覆盖过；要测的是重试**策略**，而制造一次超时或断流在真隧道上做不到
// 确定性复现。
type fakePusher struct {
	mu       sync.Mutex
	nodes    []string
	attempts map[string]int
	// outcomes[node] 是这个节点第 n 次被推时的结果；用完最后一个就一直用它。
	outcomes map[string][]tunnel.PushOutcome
}

func newFakePusher(nodes ...string) *fakePusher {
	return &fakePusher{
		nodes:    nodes,
		attempts: map[string]int{},
		outcomes: map[string][]tunnel.PushOutcome{},
	}
}

func (f *fakePusher) plan(node string, outs ...tunnel.PushOutcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outcomes[node] = outs
}

func (f *fakePusher) OnlineNodes() []string { return f.nodes }

func (f *fakePusher) Push(_ context.Context, node, _ string, _, _ []byte, _ tunnel.ResourceCounts, _ tunnel.UpstreamCert, _ time.Duration) tunnel.PushOutcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.attempts[node]
	f.attempts[node]++
	outs := f.outcomes[node]
	if len(outs) == 0 {
		return tunnel.PushOutcome{OK: true, Detail: "1ms", Responded: true}
	}
	if n >= len(outs) {
		n = len(outs) - 1
	}
	return outs[n]
}

func (f *fakePusher) attemptsFor(node string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts[node]
}

func timeout() tunnel.PushOutcome {
	return tunnel.PushOutcome{OK: false, Detail: "deadline exceeded", Responded: false}
}
func rejected() tunnel.PushOutcome {
	return tunnel.PushOutcome{OK: false, Detail: "unknown handler", Responded: true}
}
func applied() tunnel.PushOutcome {
	return tunnel.PushOutcome{OK: true, Detail: "31ms", Responded: true}
}

func newSched(t *testing.T, p deploy.Pusher) (*deploy.Scheduler, *store.Store) {
	t.Helper()
	st := testdb.New(t)
	ctx := context.Background()

	for _, id := range p.OnlineNodes() {
		if err := st.UpsertNode(ctx, store.NodeSpec{
			NodeID: id, City: "香港", Vendor: "v", Line: "l", PublicIP: "203.0.113.7",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateRoute(ctx, model.Route{
		Domain: "api.example.com", Upstream: "127.0.0.1:8080", BlockMode: model.BlockAbort,
	}); err != nil {
		t.Fatal(err)
	}

	return &deploy.Scheduler{
		Store: st, Pusher: p,
		Render:       render.Options{HTTPListen: ":18080"},
		RetryBackoff: 5 * time.Millisecond,
	}, st
}

func resultsOf(t *testing.T, st *store.Store, id int64) map[string]store.DeployResult {
	t.Helper()
	_, rs, err := st.GetDeploy(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.DeployResult{}
	for _, r := range rs {
		out[r.Node] = r
	}
	return out
}

// 传输层失败会被重试，且首轮结束时那一行是「重试中」。
//
// 前端的落定条件是「全部终态且无重试中」——首轮 2/2 回报完但仍有重试中时，
// 弹层不该落定。
func TestTransportFailureIsRetriedAndMarkedRetrying(t *testing.T) {
	p := newFakePusher("node-a", "node-b")
	p.plan("node-b", timeout(), timeout(), applied()) // 第三次才成功
	sched, st := newSched(t, p)

	res, issues, err := sched.Deploy(context.Background(), "abiu", []string{"route:api.example.com"})
	if err != nil || len(issues) > 0 {
		t.Fatalf("err=%v issues=%v", err, issues)
	}

	// 首轮：a 成功，b 失败但标为重试中。
	first := resultsOf(t, st, res.DeployID)
	if first["node-a"].State != "ok" {
		t.Errorf("node-a = %+v", first["node-a"])
	}
	if first["node-b"].State != "fail" || !first["node-b"].Retrying {
		t.Fatalf("node-b 首轮应当是 fail 且 retrying=true，实际 %+v", first["node-b"])
	}

	sched.Retries().Wait()

	final := resultsOf(t, st, res.DeployID)
	if final["node-b"].State != "ok" {
		t.Fatalf("重试后 node-b 应当成功，实际 %+v", final["node-b"])
	}
	if final["node-b"].Retrying {
		t.Error("成功之后不该还标着重试中")
	}
	if got := p.attemptsFor("node-b"); got != 3 {
		t.Errorf("node-b 被推了 %d 次，想要 3（首轮 + 两次重试）", got)
	}
	// 计数要跟着重试变，不能停在首轮的账上。
	d, _, err := st.GetDeploy(context.Background(), res.DeployID)
	if err != nil {
		t.Fatal(err)
	}
	if d.OKCount != 2 || d.FailCount != 0 {
		t.Errorf("重试成功后 ok=%d fail=%d，想要 2/0", d.OKCount, d.FailCount)
	}
}

// Caddy 拒绝的配置**不重试**（ADR-0005）：同一份字节喂给同一个 Caddy
// 必然得到同样的拒绝，重试只会在日志里刷 5 遍一模一样的报错。
func TestCaddyRejectionIsNotRetried(t *testing.T) {
	p := newFakePusher("node-a")
	p.plan("node-a", rejected())
	sched, st := newSched(t, p)

	res, _, err := sched.Deploy(context.Background(), "abiu", []string{"route:api.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	sched.Retries().Wait()

	r := resultsOf(t, st, res.DeployID)["node-a"]
	if r.Retrying {
		t.Fatal("Caddy 拒绝的配置不该被标为重试中")
	}
	if r.Detail != "unknown handler" {
		t.Errorf("detail = %q，应当原样保留 Caddy 的报错", r.Detail)
	}
	if got := p.attemptsFor("node-a"); got != 1 {
		t.Errorf("被推了 %d 次，想要 1（不重试）", got)
	}
}

// 重试次数用尽后转终态，并说明重试过。
func TestRetriesExhaustBecomeTerminal(t *testing.T) {
	p := newFakePusher("node-a")
	p.plan("node-a", timeout()) // 一直超时
	sched, st := newSched(t, p)

	res, _, err := sched.Deploy(context.Background(), "abiu", []string{"route:api.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	sched.Retries().Wait()

	r := resultsOf(t, st, res.DeployID)["node-a"]
	if r.Retrying {
		t.Fatal("次数用尽后不该还标着重试中——那会让弹层永远不落定")
	}
	if got := p.attemptsFor("node-a"); got != 6 {
		t.Errorf("被推了 %d 次，想要 6（首轮 + 5 次重试）", got)
	}
	if r.Detail == "deadline exceeded" {
		t.Error("终态的 detail 应当说明重试过，否则看不出系统努力过")
	}
}

// **一次迟到的重试不能把旧配置盖到已经拿到新版本的节点上。**
//
// 那是把节点推回过去，而现象是「某台机器莫名其妙跑着旧配置」，
// 且下发记录里一切正常——最难排查的一类。
func TestStaleRetryIsAbandonedWhenBaselineMovesOn(t *testing.T) {
	p := newFakePusher("node-a", "node-b")
	p.plan("node-b", timeout()) // b 一直不回应
	sched, st := newSched(t, p)
	ctx := context.Background()

	first, _, err := sched.Deploy(ctx, "abiu", []string{"route:api.example.com"})
	if err != nil {
		t.Fatal(err)
	}

	// 第二次下发：b 这次好了。它会取消上一次还在飞的补推。
	p.plan("node-b", applied())
	second, _, err := sched.Deploy(ctx, "abiu", []string{"route:api.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	sched.Retries().Wait()

	if first.CfgVersion == second.CfgVersion {
		t.Fatal("两次下发应当是不同的版本")
	}

	old := resultsOf(t, st, first.DeployID)["node-b"]
	if old.Retrying {
		t.Fatal("被取代的下发不该留着「重试中」——那次重试再也不会发生了")
	}

	// 基线必须是第二次那一版，没有被迟到的重试推回去。
	base, err := st.Baseline(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if base != second.CfgVersion {
		t.Fatalf("基线 = %q，想要第二次下发的 %q", base, second.CfgVersion)
	}
	var nodeVer string
	if err := st.Pool.QueryRow(ctx,
		`SELECT cfg_version FROM edge_nodes WHERE id = 'node-b'`).Scan(&nodeVer); err != nil {
		t.Fatal(err)
	}
	if nodeVer != second.CfgVersion {
		t.Fatalf("node-b 的版本 = %q，被推回到了旧版本", nodeVer)
	}
}

// 主控重启会丢掉内存里的重试队列。留在库里的「重试中」会让 phase 永远
// 停在 running，弹层永远不落定，而它等的那次重试再也不会发生。
func TestClearStaleRetriesUnsticksPhase(t *testing.T) {
	p := newFakePusher("node-a")
	p.plan("node-a", timeout())
	sched, st := newSched(t, p)
	ctx := context.Background()

	res, _, err := sched.Deploy(ctx, "abiu", []string{"route:api.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	// 不等补推结束，直接模拟重启。
	sched.Retries().CancelAll()

	if _, err := st.ClearStaleRetries(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range resultsOf(t, st, res.DeployID) {
		if r.Retrying {
			t.Fatalf("清理后不该还有重试中的行: %+v", r)
		}
	}
	sched.Retries().Wait()
}
