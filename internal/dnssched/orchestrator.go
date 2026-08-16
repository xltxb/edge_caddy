package dnssched

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/xltxb/edge_caddy/internal/dnsprovider"
	"github.com/xltxb/edge_caddy/internal/model"
)

// Store 是调度需要的存储能力。
type Store interface {
	ListNodes(ctx context.Context) ([]model.Node, error)
	ListWeights(ctx context.Context, domain string) ([]model.DNSWeight, error)
}

type Deps struct {
	Store    Store
	Provider dnsprovider.Provider
	Logger   *slog.Logger
}

type Orchestrator struct {
	st  Store
	p   dnsprovider.Provider
	log *slog.Logger
}

func New(d Deps) *Orchestrator {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Orchestrator{st: d.Store, p: d.Provider, log: log}
}

// Status 是「库里的权重」与「线上实际解析」的对照。
type Status struct {
	Domain  string                `json:"domain"`
	Planned []dnsprovider.Target  `json:"planned"`
	Live    []dnsprovider.ARecord `json:"live"`
	Drift   Drift                 `json:"drift"`
}

// ErrNoProvider 表示还没配 DNS 服务商。
var ErrNoProvider = errors.New("尚未配置 DNS 服务商")

// entries 把库里的权重与节点在线状态拼成解析条目。
//
// 在线状态来自健康巡检，不另填一份：两份状态迟早会不一致，
// 而「界面上显示在线、解析里已经摘掉」这种不一致最难查。
func (o *Orchestrator) entries(ctx context.Context, domain string) ([]Entry, error) {
	nodes, err := o.st.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取节点: %w", err)
	}
	byID := map[string]model.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}

	ws, err := o.st.ListWeights(ctx, domain)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(ws))
	for _, w := range ws {
		n, known := byID[w.NodeID]
		if !known {
			// 节点被删掉之后权重行可能还在。拿一个不存在的节点去生成解析记录，
			// 会往线上写一条指向空 IP 的记录。
			o.log.Warn("解析权重指向了已不存在的节点，已忽略",
				"domain", domain, "node_id", w.NodeID)
			continue
		}
		out = append(out, Entry{
			NodeID: w.NodeID, IP: n.PublicIP, Line: dnsprovider.Line(w.Line),
			Weight: w.Weight, Online: n.Status != "down",
		})
	}
	return out, nil
}

// Plan 算出将要下发的解析计划。
func (o *Orchestrator) Plan(ctx context.Context, domain string) ([]dnsprovider.Target, error) {
	es, err := o.entries(ctx, domain)
	if err != nil {
		return nil, err
	}
	return Plan(es)
}

// Apply 把计划真正落到服务商。
//
// 落地失败时**明确报错**，不假装成功：假装成功是这类界面最糟的失败方式——
// 人以为改好了就走了，而线上一点没变。
func (o *Orchestrator) Apply(ctx context.Context, domain string) error {
	if o.p == nil {
		return ErrNoProvider
	}
	if !o.p.SupportsWeights() {
		// 悄悄按等权重写下去的话，人会以为权重配好了，而实际流量是平均分的
		return fmt.Errorf("%s 不支持加权解析，无法按权重下发（%w）", o.p.Name(), dnsprovider.ErrNotSupported)
	}
	targets, err := o.Plan(ctx, domain)
	if err != nil {
		return err
	}
	if err := o.p.ApplyPlan(ctx, domain, targets); err != nil {
		return fmt.Errorf("下发 %s 的解析失败：%w", domain, err)
	}
	o.log.Info("解析已下发", "domain", domain, "targets", len(targets), "provider", o.p.Name())
	return nil
}

// Status 读回「库里 vs 线上」的对照。
//
// 读不到线上记录时**报错**，不能退化成「没有漂移」：报「一致」会让人以为解析
// 是对的，而实际上我们根本没看到线上是什么样。
func (o *Orchestrator) Status(ctx context.Context, domain string) (Status, error) {
	if o.p == nil {
		return Status{}, ErrNoProvider
	}
	planned, err := o.Plan(ctx, domain)
	if err != nil {
		return Status{}, err
	}
	live, err := o.p.ListA(ctx, domain)
	if err != nil {
		return Status{}, fmt.Errorf("读取 %s 的线上解析失败：%w", domain, err)
	}
	return Status{
		Domain: domain, Planned: planned, Live: live,
		Drift: Diff(planned, live),
	}, nil
}
