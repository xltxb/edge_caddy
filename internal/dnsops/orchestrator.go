// Package dnsops 把库里的状态、规划层与服务商适配拼起来。
//
// 它单独一层是因为 dnssched 必须保持**纯算术、不碰存储**：归一化的难点是
// 边界正确，而边界正确最好用不需要数据库的测试来钉。让规划层依赖仓储会
// 把那些测试拖进「先建库、再造数据」的流程里，跑得慢、写得也啰嗦。
package dnsops

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/xltxb/edge_caddy/internal/dnsctl"
	"github.com/xltxb/edge_caddy/internal/dnssched"
	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
)

// Orchestrator 把「库里的权重与节点状态」变成「服务商上的解析安排」。
//
// 它同时是 health 那边 DNSDetacher 的实现：节点被判离线时把它摘出解析，
// 恢复时放回去——两件事走的是同一条同步路径，因为它们描述的是同一件事
// （「现在谁该分到流量」），拆成两套逻辑迟早会给出不一致的结果。
type Orchestrator struct {
	Store  *store.Store
	Sealer *secret.Sealer
	Log    *slog.Logger

	mu sync.Mutex
}

// ErrNoProvider 表示还没有配置 DNS 服务商。
//
// 单独一个错误：调用方据此把文案写成「解析未变动（未配置服务商）」
// 而不是「摘除失败」——后者会让人去查凭证、查网络，而根本没配。
var ErrNoProvider = fmt.Errorf("尚未配置 DNS 服务商")

func (o *Orchestrator) logger() *slog.Logger {
	if o.Log != nil {
		return o.Log
	}
	return slog.Default()
}

// Provider 装配当前配置的服务商客户端。没配时返回 ErrNoProvider。
func (o *Orchestrator) Provider(ctx context.Context) (dnsctl.Provider, store.DNSProviderSettings, error) {
	cfg, err := o.Store.GetDNSProvider(ctx, o.Sealer)
	if err != nil {
		return nil, cfg, err
	}
	if cfg.Kind == "" || cfg.Domain == "" || cfg.Credential == "" {
		return nil, cfg, ErrNoProvider
	}

	switch cfg.Kind {
	case "dnspod":
		return dnsctl.NewDNSPod(cfg.Credential, cfg.Domain, cfg.SubName), cfg, nil
	case "cloudflare":
		cf := dnsctl.NewCloudflare(cfg.AccountID, cfg.ZoneID, hostname(cfg))
		if cfg.CredentialMode == "global_key" {
			cf.Email, cf.GlobalKey = cfg.Email, cfg.Credential
		} else {
			cf.Token = cfg.Credential
		}
		return cf, cfg, nil
	default:
		return nil, cfg, fmt.Errorf("未知的 DNS 服务商 %q", cfg.Kind)
	}
}

func hostname(cfg store.DNSProviderSettings) string {
	if cfg.SubName == "" || cfg.SubName == "@" {
		return cfg.Domain
	}
	return cfg.SubName + "." + cfg.Domain
}

// CurrentPlan 按库里的权重与节点状态算出当前应有的安排。
func (o *Orchestrator) CurrentPlan(ctx context.Context, weights dnssched.Weights) (dnssched.Plan, error) {
	cfg, err := o.Store.GetDNSProvider(ctx, nil)
	if err != nil {
		return dnssched.Plan{}, err
	}
	if weights == nil {
		w, err := o.Store.GetDNSWeights(ctx)
		if err != nil {
			return dnssched.Plan{}, err
		}
		weights = dnssched.Weights(w)
	}
	nodes, err := o.Store.ListNodes(ctx)
	if err != nil {
		return dnssched.Plan{}, err
	}
	states := make([]dnssched.NodeState, 0, len(nodes))
	for _, n := range nodes {
		states = append(states, dnssched.NodeState{
			ID: n.ID, IP: n.PublicIP, DNSEnabled: n.DNSEnabled, Status: n.Status,
		})
	}
	return dnssched.Build(hostname(cfg), weights, states), nil
}

// Sync 把当前应有的安排推到服务商。weights 为 nil 时用库里的。
func (o *Orchestrator) Sync(ctx context.Context, weights dnssched.Weights) error {
	// 串行化：自愈与人工改权重可能同时发生，两份安排交错推上去
	// 会让服务商上留下一个谁也没打算要的中间状态。
	o.mu.Lock()
	defer o.mu.Unlock()

	err := o.syncOnce(ctx, weights)

	// **每次同步都记下结果**，无论成败。界面上那个「已退出解析」徽标是常驻的，
	// 而一次请求的响应会消失——不落库的话，一次失败的同步会留下一个一直撒谎
	// 到下次有人再点开关为止的说法。
	st := store.DNSSyncState{OK: err == nil, At: time.Now(), Detail: "解析安排已同步到服务商"}
	if err != nil {
		st.Detail = err.Error()
	}
	if perr := o.Store.PutDNSSync(context.WithoutCancel(ctx), st); perr != nil {
		o.logger().Error("记录解析同步结果失败", "err", perr)
	}
	return err
}

func (o *Orchestrator) syncOnce(ctx context.Context, weights dnssched.Weights) error {
	provider, _, err := o.Provider(ctx)
	if err != nil {
		return err
	}
	plan, err := o.CurrentPlan(ctx, weights)
	if err != nil {
		return err
	}
	return provider.Sync(ctx, plan)
}

// Detach 把一个节点摘出解析。实现 health.DNSDetacher。
//
// 它不改 dns_enabled——那一步由 health 在同一个事务里做过了。
// 这里只负责让服务商侧跟上。
func (o *Orchestrator) Detach(ctx context.Context, nodeID string) error {
	o.logger().Info("摘除解析", "node", nodeID)
	return o.Sync(ctx, nil)
}

func (o *Orchestrator) Attach(ctx context.Context, nodeID string) error {
	o.logger().Info("恢复解析", "node", nodeID)
	return o.Sync(ctx, nil)
}

// Caps 说明当前服务商能做到什么。没配时返回一个空能力，
// 让界面能说「还没配服务商」而不是显示一堆不知道能不能用的输入框。
func (o *Orchestrator) Caps(ctx context.Context) dnsctl.Caps {
	provider, _, err := o.Provider(ctx)
	if err != nil {
		return dnsctl.Caps{Notes: "尚未配置 DNS 服务商，权重只会保存在本地，不会推到任何地方。"}
	}
	return provider.Caps()
}
