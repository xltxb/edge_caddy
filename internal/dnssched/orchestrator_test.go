package dnssched_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/dnsprovider"
	"github.com/xltxb/edge_caddy/internal/dnssched"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/store"
)

// fakeProvider 记录落地调用。真正的 HTTP 对接在 dnsprovider 包里用真服务端
// 测过；这里要验的是**编排**：谁在线、算成什么计划、失败了怎么办。
type fakeProvider struct {
	mu      sync.Mutex
	applied [][]dnsprovider.Target
	live    []dnsprovider.ARecord
	err     error
	listErr error
}

func (f *fakeProvider) Name() string          { return "Fake" }
func (f *fakeProvider) SupportsLines() bool   { return true }
func (f *fakeProvider) SupportsWeights() bool { return true }

func (f *fakeProvider) SetTXT(context.Context, string, string) error    { return nil }
func (f *fakeProvider) RemoveTXT(context.Context, string, string) error { return nil }

func (f *fakeProvider) ListA(context.Context, string) ([]dnsprovider.ARecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live, f.listErr
}

func (f *fakeProvider) ApplyPlan(_ context.Context, _ string, ts []dnsprovider.Target) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.applied = append(f.applied, ts)
	// 落地成功后线上就是这份
	f.live = nil
	for i, t := range ts {
		f.live = append(f.live, dnsprovider.ARecord{
			ID: string(rune('1' + i)), Sub: "@", Value: t.IP, Line: t.Line, Weight: t.Weight,
		})
	}
	return nil
}

func (f *fakeProvider) applyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied)
}

func (f *fakeProvider) lastPlan() []dnsprovider.Target {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.applied) == 0 {
		return nil
	}
	return f.applied[len(f.applied)-1]
}

func newOrch(t *testing.T, p dnsprovider.Provider) (*dnssched.Orchestrator, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return dnssched.New(dnssched.Deps{Store: st, Provider: p}), st
}

func seedNodes(t *testing.T, st *store.Store, nodes ...model.Node) {
	t.Helper()
	ctx := context.Background()
	for _, n := range nodes {
		if err := st.UpsertNodeSeen(ctx, n.ID, "cfg-1", time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().ExecContext(ctx,
			`UPDATE edge_nodes SET public_ip = ?, status = ? WHERE id = ?`, n.PublicIP, n.Status, n.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func seedWeights(t *testing.T, st *store.Store, domain string, ws ...model.DNSWeight) {
	t.Helper()
	if err := st.PutWeights(context.Background(), domain, ws); err != nil {
		t.Fatal(err)
	}
}

// 保存后**真的调用**服务商 API。
func TestApplyCallsProvider(t *testing.T) {
	p := &fakeProvider{}
	o, st := newOrch(t, p)
	ctx := context.Background()

	seedNodes(t, st,
		model.Node{ID: "node-a", PublicIP: "1.1.1.1", Status: "ok"},
		model.Node{ID: "node-b", PublicIP: "2.2.2.2", Status: "ok"})
	seedWeights(t, st, "cdn.example.com",
		model.DNSWeight{NodeID: "node-a", Line: "电信", Weight: 60},
		model.DNSWeight{NodeID: "node-b", Line: "电信", Weight: 40})

	if err := o.Apply(ctx, "cdn.example.com"); err != nil {
		t.Fatal(err)
	}
	if p.applyCount() != 1 {
		t.Fatalf("应调用一次服务商 API，实际 %d 次", p.applyCount())
	}
	got := byNode(p.lastPlan())
	if got["node-a"].Weight != 60 || got["node-b"].Weight != 40 {
		t.Errorf("下发的权重不对：%+v", p.lastPlan())
	}
	if got["node-a"].IP != "1.1.1.1" {
		t.Errorf("应带上节点 IP，实际 %q", got["node-a"].IP)
	}
}

// 离线节点自动退出解析。节点状态来自健康巡检，不是另填一份。
func TestOfflineNodeExitsResolution(t *testing.T) {
	p := &fakeProvider{}
	o, st := newOrch(t, p)
	ctx := context.Background()

	seedNodes(t, st,
		model.Node{ID: "node-a", PublicIP: "1.1.1.1", Status: "ok"},
		model.Node{ID: "node-b", PublicIP: "2.2.2.2", Status: "down"})
	seedWeights(t, st, "cdn.example.com",
		model.DNSWeight{NodeID: "node-a", Line: "电信", Weight: 50},
		model.DNSWeight{NodeID: "node-b", Line: "电信", Weight: 50})

	if err := o.Apply(ctx, "cdn.example.com"); err != nil {
		t.Fatal(err)
	}
	plan := p.lastPlan()
	if len(plan) != 1 || plan[0].NodeID != "node-a" {
		t.Fatalf("离线节点应退出解析，实际 %+v", plan)
	}
	if plan[0].Weight != 100 {
		t.Errorf("剩下的唯一节点应归一化到 100，实际 %d", plan[0].Weight)
	}
}

// **服务商 API 失败时明确报错，不假装保存成功**。
//
// 假装成功是这类界面最糟的失败方式：人以为改好了就走了，而线上一点没变。
func TestProviderFailureIsSurfaced(t *testing.T) {
	p := &fakeProvider{err: errors.New("DNSPod 返回错误（code -1）：登录失败")}
	o, st := newOrch(t, p)
	ctx := context.Background()
	seedNodes(t, st, model.Node{ID: "node-a", PublicIP: "1.1.1.1", Status: "ok"})
	seedWeights(t, st, "cdn.example.com",
		model.DNSWeight{NodeID: "node-a", Line: "电信", Weight: 100})

	err := o.Apply(ctx, "cdn.example.com")
	if err == nil {
		t.Fatal("服务商失败时必须报错，不能假装保存成功")
	}
	if !containsStr(err.Error(), "登录失败") {
		t.Errorf("应带上服务商给的原因，实际 %v", err)
	}
}

// 节点信息不全（没有 IP）时当场拒绝，不去调服务商。
func TestNodeWithoutIPIsRejectedBeforeCallingProvider(t *testing.T) {
	p := &fakeProvider{}
	o, st := newOrch(t, p)
	ctx := context.Background()
	seedNodes(t, st, model.Node{ID: "node-a", PublicIP: "", Status: "ok"})
	seedWeights(t, st, "cdn.example.com",
		model.DNSWeight{NodeID: "node-a", Line: "电信", Weight: 100})

	if err := o.Apply(ctx, "cdn.example.com"); err == nil {
		t.Fatal("没有 IP 的节点应当场拒绝")
	}
	if p.applyCount() != 0 {
		t.Error("拒绝之后不该再去调服务商")
	}
}

// 权重表里有、节点表里没有的条目要忽略掉。
//
// 节点被删掉之后权重行可能还在。拿一个不存在的节点去生成解析记录，
// 会往线上写一条指向空 IP 的记录。
func TestUnknownNodeInWeightsIsIgnored(t *testing.T) {
	p := &fakeProvider{}
	o, st := newOrch(t, p)
	ctx := context.Background()
	seedNodes(t, st, model.Node{ID: "node-a", PublicIP: "1.1.1.1", Status: "ok"})
	seedWeights(t, st, "cdn.example.com",
		model.DNSWeight{NodeID: "node-a", Line: "电信", Weight: 50},
		model.DNSWeight{NodeID: "node-ghost", Line: "电信", Weight: 50})

	if err := o.Apply(ctx, "cdn.example.com"); err != nil {
		t.Fatal(err)
	}
	plan := p.lastPlan()
	if len(plan) != 1 || plan[0].NodeID != "node-a" {
		t.Fatalf("已不存在的节点不该进解析，实际 %+v", plan)
	}
}

// Status 能读回「库里 vs 线上」的对照。
func TestStatusReportsDrift(t *testing.T) {
	p := &fakeProvider{live: []dnsprovider.ARecord{
		{ID: "1", Sub: "@", Value: "1.1.1.1", Line: dnsprovider.LineTelecom, Weight: 30},
	}}
	o, st := newOrch(t, p)
	ctx := context.Background()
	seedNodes(t, st, model.Node{ID: "node-a", PublicIP: "1.1.1.1", Status: "ok"})
	seedWeights(t, st, "cdn.example.com",
		model.DNSWeight{NodeID: "node-a", Line: "电信", Weight: 100})

	s, err := o.Status(ctx, "cdn.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Drift.Drifted() {
		t.Fatal("库里 100、线上 30，应报告漂移")
	}
	if len(s.Planned) != 1 || s.Planned[0].Weight != 100 {
		t.Errorf("应给出将要下发的计划，实际 %+v", s.Planned)
	}
	if len(s.Live) != 1 || s.Live[0].Weight != 30 {
		t.Errorf("应给出线上实际，实际 %+v", s.Live)
	}
}

// 读线上失败时**不能**报成「没有漂移」。
//
// 报「一致」会让人以为解析是对的，而实际上我们根本没看到线上是什么样。
func TestStatusSurfacesListError(t *testing.T) {
	p := &fakeProvider{listErr: errors.New("凭据无效")}
	o, st := newOrch(t, p)
	ctx := context.Background()
	seedNodes(t, st, model.Node{ID: "node-a", PublicIP: "1.1.1.1", Status: "ok"})
	seedWeights(t, st, "cdn.example.com",
		model.DNSWeight{NodeID: "node-a", Line: "电信", Weight: 100})

	if _, err := o.Status(ctx, "cdn.example.com"); err == nil {
		t.Fatal("读不到线上记录时必须报错——报「一致」会让人以为解析是对的")
	}
}

// 服务商不支持权重时当场说清楚，而不是把权重悄悄丢掉。
func TestProviderWithoutWeightSupportIsRejected(t *testing.T) {
	o, st := newOrch(t, dnsprovider.NewCloudflare(dnsprovider.CloudflareConfig{APIToken: "t"}))
	ctx := context.Background()
	seedNodes(t, st, model.Node{ID: "node-a", PublicIP: "1.1.1.1", Status: "ok"})
	seedWeights(t, st, "cdn.example.com",
		model.DNSWeight{NodeID: "node-a", Line: "电信", Weight: 100})

	err := o.Apply(ctx, "cdn.example.com")
	if err == nil {
		t.Fatal("服务商不支持加权解析时应拒绝，而不是把权重悄悄丢掉")
	}
}
