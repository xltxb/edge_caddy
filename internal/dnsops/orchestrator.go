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
	"strings"
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

	// BaseOverride 把服务商 API 指向别处。**只有测试会设它**，
	// 生产路径上它是空的，各家走自己的官方地址。
	//
	// 在生产结构体上开这个口子是有代价的，但不开的代价更大：下线的
	// 「排空连接」这一步只在**上一步真的摘掉了解析**时才执行，而 e2e 里
	// 没有服务商可配，于是那条分支一次也走不到。一个只在「没配服务商」
	// 这个分支上被测过的功能，等于没测——而它恰恰是最需要端到端验的那种。
	BaseOverride string

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
	// **判据只有一个来源**（store.MissingFields），校验那一侧用的是同一个。
	// 分成两处写迟早会分叉，而分叉的症状是「设置页说已配置、这里说没配置」
	// —— 两句在各自的口径下都对，而它们对不上账。
	if !cfg.Usable() {
		return nil, cfg, ErrNoProvider
	}

	targets := cfg.EffectiveTargets()
	if len(targets) == 0 {
		return nil, cfg, ErrNoProvider
	}
	p, err := o.providerFor(cfg, targets[0])
	return p, cfg, err
}

// Configured 说现在有没有一个能用的服务商配置。
//
// **它存在是为了让背景动作先问后做。** Sync 无论成败都会落一条同步状态
// （那是有意的：界面上那个徽标是常驻的，一次失败不能只活在响应里）。
// 而「没配服务商」时那条状态记的是一件没发生过的事 —— 人点开关时
// 记下来是对的（他问了，这是回答），一台机器接入时记下来就把
// 「从没同步过」变成了一个像模像样的时间戳。
func (o *Orchestrator) Configured(ctx context.Context) bool {
	_, _, err := o.Provider(ctx)
	return err == nil
}

// providerFor 为**一个目标**装配一个服务商客户端。
//
// **一个目标一个实例**：Cloudflare 的 zone_id 与要写的主机名都在实例上，
// 而不同顶级域是不同的 zone。共用一个实例的话，第二个域名会被写进
// 第一个域名的 zone —— 那不会报错，Cloudflare 会老老实实建一条
// `b.other.com` 记录在 `webjump.top` 的 zone 里，而它永远不会被解析到。
func (o *Orchestrator) providerFor(cfg store.DNSProviderSettings, t store.DNSTarget) (dnsctl.Provider, error) {
	switch cfg.Kind {
	case "dnspod":
		p := dnsctl.NewDNSPod(cfg.Credential, t.Domain, t.Sub)
		if o.BaseOverride != "" {
			p.Base = o.BaseOverride
		}
		return p, nil
	case "cloudflare_dns":
		cf := dnsctl.NewCloudflareDNS(t.ZoneID, t.Hostname())
		if cfg.CredentialMode == "global_key" {
			cf.Email, cf.GlobalKey = cfg.Email, cfg.Credential
		} else {
			cf.Token = cfg.Credential
		}
		if o.BaseOverride != "" {
			cf.Base = o.BaseOverride
		}
		return cf, nil
	case "cloudflare":
		cf := dnsctl.NewCloudflare(cfg.AccountID, t.ZoneID, t.Hostname())
		if cfg.CredentialMode == "global_key" {
			cf.Email, cf.GlobalKey = cfg.Email, cfg.Credential
		} else {
			cf.Token = cfg.Credential
		}
		if o.BaseOverride != "" {
			cf.Base = o.BaseOverride
		}
		return cf, nil
	default:
		return nil, fmt.Errorf("未知的 DNS 服务商 %q", cfg.Kind)
	}
}

// HostnameOf 是 sub + domain 的拼法。**导出是为了让它只有一份。**
//
// 校验那一侧要用它判「解析域名会不会撞上控制台自己的域名」，
// 而那时配置还没落库（Orchestrator.Hostname 读的是库）。
// 抄一份过去的话，两边迟早分叉，而分叉的症状是那道门放行了一个真会撞的配置。
func HostnameOf(cfg store.DNSProviderSettings) string {
	if cfg.Domain == "" {
		return ""
	}
	if cfg.SubName == "" || cfg.SubName == "@" {
		return cfg.Domain
	}
	return cfg.SubName + "." + cfg.Domain
}

func hostname(cfg store.DNSProviderSettings) string { return HostnameOf(cfg) }

// Hostname 是解析记录**实际会写到的那个名字**（sub + domain）。
//
// 把它说出来是因为这两个字段拼错的后果是静默的：
// domain 填 `cdn.example.com`、sub 又填 `cdn`，记录会建到
// `cdn.cdn.example.com` —— **推送成功、接口回 200，而人在服务商面板上
// 永远看不到它**，因为他看的是另一个名字。
//
// 服务商那边也不会拦：那是它 zone 里一个完全合法的子域名。
func (o *Orchestrator) Hostname(ctx context.Context) string {
	cfg, err := o.Store.GetDNSProvider(ctx, nil)
	if err != nil {
		return ""
	}
	return hostname(cfg)
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
	states, err := o.nodeStates(ctx)
	if err != nil {
		return dnssched.Plan{}, err
	}
	// 计划里的 Domain 用第一个目标：这一页展示的是**轮换**（所有域名共用），
	// 而域名清单由 GET /dns/weights 的 domains 单独给。
	var first string
	if ts := cfg.EffectiveTargets(); len(ts) > 0 {
		first = ts[0].Hostname()
	}
	return dnssched.Build(first, weights, states), nil
}

// nodeStates 把库里的节点转成归一化需要的形状。**两处共用**：
// CurrentPlan 与 syncOnce 都要它，而分开写的话，
// 「哪些节点算候选」这件事会有两份答案。
func (o *Orchestrator) nodeStates(ctx context.Context) ([]dnssched.NodeState, error) {
	nodes, err := o.Store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	states := make([]dnssched.NodeState, 0, len(nodes))
	for _, n := range nodes {
		states = append(states, dnssched.NodeState{
			ID: n.ID, IP: n.PublicIP, DNSEnabled: n.DNSEnabled, Status: n.Status,
			Drained: n.DrainedAt != nil,
		})
	}
	return states, nil
}

// Sync 把当前应有的安排推到服务商。weights 为 nil 时用库里的。
func (o *Orchestrator) Sync(ctx context.Context, weights dnssched.Weights) error {
	// 串行化：自愈与人工改权重可能同时发生，两份安排交错推上去
	// 会让服务商上留下一个谁也没打算要的中间状态。
	o.mu.Lock()
	defer o.mu.Unlock()

	results, err := o.syncOnce(ctx, weights)

	// **每次同步都记下结果**，无论成败。界面上那个「已退出解析」徽标是常驻的，
	// 而一次请求的响应会消失——不落库的话，一次失败的同步会留下一个一直撒谎
	// 到下次有人再点开关为止的说法。
	now := time.Now()
	st := store.DNSSyncState{OK: err == nil, At: &now, Targets: results}

	switch {
	case err != nil && len(results) == 0:
		// 连目标都没算出来（没配服务商 / 读库失败）。
		st.Detail = err.Error()
	case err != nil:
		// **部分失败要说出「几个里的几个」。**
		//
		// 只报第一条错的话，人看到一句 Cloudflare 的报错，不知道那是
		// 三个域名里的一个还是全部 —— 而这两种情况的紧急程度差很远。
		ok := 0
		for _, r := range results {
			if r.OK {
				ok++
			}
		}
		st.Detail = fmt.Sprintf("%d 个域名里 %d 个同步成功；失败的：%s",
			len(results), ok, firstFailure(results))
	default:
		// **成功时把写入的名字都记下来。**
		//
		// domain 与 sub 拼重复（`cdn.example.com` + `cdn`）时记录会建到
		// `cdn.cdn.example.com`：同步成功、ok=true、服务商也不会拦，
		// **而人在面板上永远看不到它**——他看的是另一个名字。
		//
		// 这个 detail 是常驻的（界面上那个徽标读它），所以它比一次性的响应
		// 更该带上这些名字：人来查「为什么服务商上没有」时，第一眼就该看到
		// 我们写到了哪儿。
		names := make([]string, 0, len(results))
		for _, r := range results {
			names = append(names, r.Hostname)
		}
		st.Detail = "解析安排已同步到服务商（写入 " + strings.Join(names, "、") + "）"
		// 服务商留的附注（比如把五条线合并成了并集）跟在各自的目标上，
		// 挑第一条非空的放进总说明——**不接上的话它就是个没人读的字段**。
		for _, r := range results {
			if i := strings.Index(r.Detail, "。"); i >= 0 && len(r.Detail) > i+len("。") {
				st.Detail += "。" + r.Detail[i+len("。"):]
				break
			}
		}
	}
	if perr := o.Store.PutDNSSync(context.WithoutCancel(ctx), st); perr != nil {
		o.logger().Error("记录解析同步结果失败", "err", perr)
	}
	return err
}

// syncOnce 推一次，并把服务商留下的附注一起带出来。
//
// **附注必须从这个实例上取。** 我第一版写的是在外面重新 o.Provider(ctx)
// 再读 Note()，而那会新造一个实例——上面那次同步设的附注在另一个对象上，
// 于是它恒为空串。**又一个「什么都没发生」装成「没什么可说」。**
// syncOnce 把每个目标各推一次，并把每个目标的结果分别带出来。
//
// **一个域名失败不能被淹在「整体成功」里。** 三个域名两个成功一个失败时，
// 一个布尔说不出该去看哪一个 —— 而人会按那个布尔决定要不要去查。
//
// 也不在第一个失败时就停：**剩下的域名该推还得推**。停下来的话，
// 一个域名的凭证问题会连带让另外两个也不同步，而它们本来没有任何问题。
func (o *Orchestrator) syncOnce(ctx context.Context, weights dnssched.Weights) ([]store.DNSTargetSync, error) {
	cfg, err := o.Store.GetDNSProvider(ctx, o.Sealer)
	if err != nil {
		return nil, err
	}
	if !cfg.Usable() {
		return nil, ErrNoProvider
	}
	targets := cfg.EffectiveTargets()
	if len(targets) == 0 {
		return nil, ErrNoProvider
	}

	weightsForPlan := weights
	if weightsForPlan == nil {
		if w, werr := o.Store.GetDNSWeights(ctx); werr == nil {
			weightsForPlan = dnssched.Weights(w)
		} else {
			return nil, werr
		}
	}
	nodes, err := o.nodeStates(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]store.DNSTargetSync, 0, len(targets))
	var firstErr error
	for _, t := range targets {
		st := store.DNSTargetSync{Hostname: t.Hostname()}
		provider, perr := o.providerFor(cfg, t)
		if perr != nil {
			st.Detail = perr.Error()
			out = append(out, st)
			if firstErr == nil {
				firstErr = perr
			}
			continue
		}
		plan := dnssched.Build(t.Hostname(), weightsForPlan, nodes)
		if serr := provider.Sync(ctx, plan); serr != nil {
			st.Detail = serr.Error()
			if firstErr == nil {
				firstErr = serr
			}
		} else {
			st.OK = true
			st.Detail = "已同步"
			if n, ok := provider.(Noter); ok && n.Note() != "" {
				st.Detail += "。" + n.Note()
			}
		}
		out = append(out, st)
	}
	return out, firstErr
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

// Noter 是「这次同步做了，但有件事值得说」的出口。
//
// 不是每家都有。**用可选接口而不是给 Provider 加方法**：
// 加方法会逼 DNSPod 和 Cloudflare 各写一个空实现，而空实现最容易在
// 后来真需要说点什么时被忘掉。
type Noter interface{ Note() string }

// firstFailure 是第一条失败的目标，给「部分失败」那句话用。
//
// 只给第一条是有意的：全部列出来会把一句给人扫一眼的摘要撑成一段，
// 而完整清单在 Targets 里，界面展开就能看。
func firstFailure(results []store.DNSTargetSync) string {
	for _, r := range results {
		if !r.OK {
			return r.Hostname + "（" + r.Detail + "）"
		}
	}
	return ""
}
