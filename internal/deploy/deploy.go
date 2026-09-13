// Package deploy 是下发流水线：草稿 → 校验 → 广播 → 逐节点回报 → 确立新基线。
//
// 「下发」是把选中的草稿合入基线并广播到各边缘节点的完整过程（CONTEXT.md）。
// 一次下发只携带**本次勾选**的草稿，未勾选的仍是草稿。
package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xltxb/edge_caddy/internal/dnsops"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/render"
	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/tunnel"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// PushDeadline 是单个节点的热重载超时。超过它算传输层失败（ADR-0005）。
const PushDeadline = 5 * time.Second

// Pusher 是隧道在这一层的最小面貌。抽出来只为让下发能被单独测。
type Pusher interface {
	OnlineNodes() []string
	Push(ctx context.Context, nodeID, cfgVersion string, caddyJSON, verifyRules []byte, counts tunnel.ResourceCounts, up tunnel.UpstreamCert, deadline time.Duration) tunnel.PushOutcome
}

type Scheduler struct {
	Store  *store.Store
	Pusher Pusher
	Hub    *ws.Hub
	Log    *slog.Logger
	Render render.Options

	// Sealer 用来解开服务密钥规则的共享密钥。渲染需要明文——
	// 校验端点要拿它验签，而它只在下发的载荷里出现，不经任何读接口回显。
	Sealer *secret.Sealer

	// UpstreamCA 给每个节点签回源 mTLS 的客户端证书（ADR-0008 / ADR-0009）。
	// 叶子 24 小时，随每次下发续上——吊销就退化成「停止续期」这一个动作，
	// 不需要另造 CRL/OCSP（内部 PKI 的吊销列表基本没人真部署，写了也是摆设）。
	UpstreamCA *pki.CA

	// DNS 让新节点接入后能真的进解析。窄接口与 health.DNSDetacher 是一对
	// （dnsops.Orchestrator 两边都满足）—— 那边管「摘掉与恢复」，
	// 这边管「第一次加进来」，说的是同一件事的三个时刻。
	// 留空表示没配服务商，接入照常，只是解析不动。
	DNS interface {
		Attach(ctx context.Context, nodeID string) error
		// Configured 让 NodeUp 先问后做 —— 见 dnsops.Orchestrator.Configured。
		Configured(ctx context.Context) bool
	}

	// RetryBackoff 是第一次重试前的等待，此后翻倍。留空即用默认的 1 秒。
	// 做成字段只为让重试策略能被单独测——真跑 1+2+4+8+16 秒的测试不会有人跑。
	RetryBackoff time.Duration

	// deployMu 串行化下发。它握过一整次推送（首轮 + 落库），
	// 所以不与任何别的锁合用。
	deployMu sync.Mutex

	retryOnce sync.Once
	retrier   *Retrier

	// commitFault 仅供测试注入「合入基线失败」（export_test.go）。
	// 生产装配永远不设它。
	commitFault error
}

func (s *Scheduler) baseBackoff() time.Duration {
	if s.RetryBackoff > 0 {
		return s.RetryBackoff
	}
	return defaultBaseBackoff
}

// Retries 返回后台补推器，装配时惰性建立。
func (s *Scheduler) Retries() *Retrier {
	s.retryOnce.Do(func() { s.retrier = newRetrier(s) })
	return s.retrier
}

// ErrNoOnlineNodes —— 没有在线节点时下发是个无操作。
// 静默成功会让人以为配置生效了，而实际上一台机器都没收到。
var ErrNoOnlineNodes = fmt.Errorf("没有在线节点")

// ErrNoBaseline —— 还没有过一次成功的下发，没有配置可推。
//
// 做成哨兵是因为 NodeUp 要**把它与「推失败」分开**：没有基线时这台机器
// 与集群里其他机器一样就绪（大家都还没有配置），该照常进解析；
// 而推失败意味着只有它没有配置，那时进解析就是把流量导向一台服务不了的机器。
// 靠比对错误文本来分是错的 —— 那句话哪天改个措辞，两种情况会静默合成一种。
var ErrNoBaseline = fmt.Errorf("还没有基线，先完成一次下发")

// Result 是一次下发的结果概览。
type Result struct {
	DeployID   int64
	CfgVersion string
	Targets    []string
	OKCount    int
	FailCount  int
}

// Deploy 执行一次下发。issues 非空时表示校验未过，此时**一个节点都不会被触达**。
func (s *Scheduler) Deploy(ctx context.Context, operator string, resKeys []string) (Result, []render.Issue, error) {
	// **一次只跑一次下发。**
	//
	// 没有这把锁的话，两次下发会同时往各个节点上写——而每台机器的终态取决于
	// 它自己那一侧谁后到，两次下发的记录却都会说成功（issue #32）。
	// 「两个人同时点」只是其中一条路径：人点的同时证书导入触发一次重发、
	// 或者一个人点两下，都是同一件事。
	//
	// Retries().CancelAll() 挡不住它：那一条管的是**补推**，而首轮推送不经过
	// 重试器。两者要的东西也不同——CancelAll 是「旧的别再推了」，
	// 这把锁是「新的等一等」。
	//
	// 等而不是拒：一次下发通常几秒，而「正忙，请稍后再试」会把一个本来能
	// 自己排好队的事情变成人的负担。
	s.deployMu.Lock()
	defer s.deployMu.Unlock()

	log := s.Log
	if log == nil {
		log = slog.Default()
	}

	routes, rules, pol, orphans, err := s.effective(ctx, resKeys)
	if err != nil {
		return Result{}, nil, err
	}
	// **孤儿草稿要在触达任何东西之前就拦下来。**
	// 让它走下去的话，这次下发会成功、而那份草稿会被 DeleteDrafts 删掉——
	// 人写的东西没了，且他收到的是「成功」。
	if len(orphans) > 0 {
		return Result{}, orphans, nil
	}

	certs, err := s.certsForRender(ctx)
	if err != nil {
		return Result{}, nil, err
	}

	cfg, issues := render.Render(routes, rules, certs, pol, s.Render)
	if len(issues) > 0 {
		// 校验不过即整体拒绝，不触达节点。
		return Result{}, issues, nil
	}

	// **有节点认不出这次要下发的规则类型时，整体拒绝。**
	//
	// 实测出来的：旧 Agent 收到一条它不认识的规则类型（rate_limit / geo_block）
	// 时，校验端点走 default 分支回 403 —— 那个域名的**第一个请求就被拒**。
	// 不是降级，是整站对所有人关闭，而配置看起来完全正常。
	//
	// 校验端点是 fail-closed 的（ADR-0003），那对「这个请求没有凭据」是对的；
	// 而「这个节点不认识这条规则」是另一回事 —— 把我们的版本落后变成所有
	// 访问者的 403，是把一次升级疏忽放大成一次全站故障。
	//
	// 拦在这里而不是让节点拒：节点拒的话人看到的是「下发失败」，
	// 而**已经在跑的那些节点已经应用了新配置** —— 一半节点新一半旧，
	// 那是最难查的一种状态。
	if unsup, err := s.unsupportedRules(ctx, rules); err != nil {
		return Result{}, nil, err
	} else if len(unsup) > 0 {
		return Result{}, unsup, nil
	}

	// 验签材料走旁路，不进 Caddy 配置——Admin API 能读回整份运行配置。
	verifyRules, err := json.Marshal(render.VerifyRules(rules))
	if err != nil {
		return Result{}, nil, fmt.Errorf("序列化校验规则: %w", err)
	}
	counts := tunnel.ResourceCounts{Routes: uint32(len(routes)), Rules: uint32(countEffectiveRules(rules))}

	targets := s.Pusher.OnlineNodes()
	if len(targets) == 0 {
		return Result{}, nil, ErrNoOnlineNodes
	}

	// 快照存的是**资源状态**，不是渲染后的 Caddy JSON：回滚要逐资源比对差异
	// 并写回草稿，而渲染产物是把全部资源揉在一起之后的样子，拆不回来。
	snapshot, err := json.Marshal(Snapshot{Routes: routes, Rules: rules})
	if err != nil {
		return Result{}, nil, fmt.Errorf("序列化快照: %w", err)
	}

	cfgVersion := store.NewCfgVersion()
	deployID, err := s.Store.CreateDeploy(ctx, cfgVersion, operator, resKeys, snapshot, targets)
	if err != nil {
		return Result{}, nil, fmt.Errorf("写入下发记录: %w", err)
	}

	// 新的下发开始，停掉上一次还在飞的补推。一次迟到的重试会把旧配置盖到
	// 已经拿到新版本的节点上——那是把节点推回过去。
	s.Retries().CancelAll()

	for _, n := range targets {
		s.progress(deployID, cfgVersion, n, "wait", "", false)
	}

	type outcome struct {
		node string
		out  tunnel.PushOutcome
	}
	results := make([]outcome, len(targets))
	var wg sync.WaitGroup
	for i, node := range targets {
		wg.Add(1)
		go func(i int, node string) {
			defer wg.Done()
			s.progress(deployID, cfgVersion, node, "run", "", false)
			out := s.Pusher.Push(ctx, node, cfgVersion, cfg, verifyRules, counts,
				s.upstreamCertFor(node), PushDeadline)
			results[i] = outcome{node, out}

			// **结果一到就落库**，不等其余节点。
			//
			// 契约 §2 承诺 WS 断线时降级为轮询 GET /deploys/:id，且它的字段与
			// deploy_progress 帧一一对应。攒到最后再写会让轮询在整个下发过程中
			// 什么都看不到，降级路径就成了摆设——而那恰恰是用户最需要被告知的时刻。
			state, detail := "fail", out.Detail
			if out.OK {
				state = "ok"
			}
			// 只有**传输层失败**才会被重试（ADR-0005）：节点没回应才重试，
			// 节点回应了但 Caddy 拒绝的不重试。这一位决定前端那一行显示
			// 「重试中」还是终态红字。
			retrying := !out.OK && !out.Responded
			if err := s.Store.SaveDeployResult(ctx, deployID, store.DeployResult{
				Node: node, State: state, Detail: detail, Retrying: retrying,
			}); err != nil {
				log.Error("保存下发结果失败", "node", node, "err", err)
			}
			s.progress(deployID, cfgVersion, node, state, detail, retrying)
		}(i, node)
	}
	wg.Wait()

	var okCount, failCount int
	var needRetry []string
	for _, r := range results {
		if r.out.OK {
			okCount++
			if err := s.Store.SetNodeCfgVersion(ctx, r.node, cfgVersion); err != nil {
				log.Error("更新节点配置版本失败", "node", r.node, "err", err)
			}
		} else {
			failCount++
			if !r.out.Responded {
				needRetry = append(needRetry, r.node)
			}
		}
	}

	if err := s.Store.FinishDeploy(ctx, deployID, okCount, failCount); err != nil {
		log.Error("收尾下发记录失败", "err", err)
	}

	if okCount > 0 {
		// **把本次下发的内容合入 live。**
		//
		// 草稿是叠加在 live 之上的 Partial；下发之后那些改动已经是基线的一部分，
		// 不落回 live 就等于：节点上跑着新配置，而真相源里还是旧值，
		// 下一次下发会把旧值推回去。而现象是「我明明改过、也下发成功了，
		// 怎么又变回去了」——中间没有任何报错。
		if err := s.Store.CommitDeploy(ctx, store.CommitDeploy{
			ResKeys: resKeys, Routes: routes, Rules: rules,
			CfgVersion: cfgVersion, DeployID: deployID,
			Sealer: s.Sealer,
			Fault:  s.commitFaultFn(),
		}); err != nil {
			// **失败就停在这里，下面一步都不走。**
			//
			// 走下去的话：基线前进（宣称「live 渲染出来就是这一版」——假的）、
			// 草稿被删（用户的改动从此哪儿都不在）。节点上跑着新配置、
			// live 表还是旧值、界面说成功 —— 下一次任何下发把旧值推回去，
			// 「成功的假象里最贵的一种：它同时是数据丢失」（issue #31）。
			//
			// 停下来之后的状态是真话：草稿还在（人能重新下发）、基线没动
			// （节点比基线新，漂移检测会把这件事标出来）。事件必须写 ——
			// 只进 Error 日志的话，没人会在出事前发现。
			log.Error("把下发内容合入基线失败", "err", err)
			s.event(ctx, "", "crit", fmt.Sprintf(
				"配置 %s 已推到 %d 个节点，但合入基线失败：%v —— "+
					"草稿保留、基线未前进，请重新下发；在那之前节点上跑的是新配置，"+
					"而库里还是旧值", cfgVersion, okCount, err))
			return Result{
				DeployID: deployID, CfgVersion: cfgVersion, Targets: targets,
				OKCount: okCount, FailCount: failCount,
			}, nil, nil
		}

		// 基线、三个版本号、清草稿都在上面那一个事务里。
		//
		// 它们原先是五个散装调用，每个失败都只 log.Error 然后继续——
		// 而「至少有一台真的应用了，基线才前进」这条判断仍然成立：
		// 它就是上面那个 okCount > 0 的 if。
	}

	// 首轮跑完就返回，掉队的交给后台补推：5 次指数退避最长要一分多钟，
	// 而 PRD 要求单次全网推送 6 节点 ≤10s 完成反馈。进度继续经 WS 推。
	s.Retries().enqueue(retryJob{
		deployID: deployID, cfgVersion: cfgVersion,
		caddyJSON: cfg, verifyRules: verifyRules, counts: counts, nodes: needRetry,
	})

	msg := fmt.Sprintf("配置 %s 下发完成，%d/%d 节点", cfgVersion, okCount, len(targets))
	if len(needRetry) > 0 {
		msg = fmt.Sprintf("配置 %s 首轮 %d/%d 节点，%d 个节点重试中",
			cfgVersion, okCount, len(targets), len(needRetry))
	}
	s.event(ctx, "", eventKind(okCount, failCount), msg)

	return Result{
		DeployID: deployID, CfgVersion: cfgVersion, Targets: targets,
		OKCount: okCount, FailCount: failCount,
	}, nil, nil
}

func eventKind(ok, fail int) string {
	switch {
	case fail == 0:
		return "ok"
	case ok == 0:
		return "crit"
	default:
		return "warn"
	}
}

// effective = 基线 + **本次勾选**的草稿。未勾选的草稿不参与本次渲染。
//
// 第四个返回值是**勾了、而底下没有资源**的那些 res_key。它们必须被说出来：
// 草稿是「在已有资源上的改动」（Partial），没有底子就合并不出东西，
// 而下面三个循环都是「遍历 live、有草稿就套上去」——**没有对应 live 的草稿
// 会被静默跳过**。下发照常成功，随后 DeleteDrafts 把那份草稿删掉。
//
// 也就是：写了一条新规则、预览说没问题、下发说成功，然后什么都没有、草稿也没了。
// **成功的假象里最贵的一种：它同时是数据丢失。**
func (s *Scheduler) effective(ctx context.Context, resKeys []string) ([]model.Route, []model.Rule, render.Policies, []render.Issue, error) {
	var pol render.Policies
	liveRoutes, err := s.Store.ListRoutes(ctx)
	if err != nil {
		return nil, nil, pol, nil, fmt.Errorf("读取路由: %w", err)
	}
	// 渲染需要共享密钥的明文：校验端点要拿它验签。它只出现在下发的载荷里，
	// 不经任何读接口回显。
	liveRules, err := s.Store.ListRules(ctx, s.Sealer)
	if err != nil {
		return nil, nil, pol, nil, fmt.Errorf("读取访问规则: %w", err)
	}
	livePolicies, err := s.Store.ListPolicies(ctx)
	if err != nil {
		return nil, nil, pol, nil, fmt.Errorf("读取全局策略: %w", err)
	}
	drafts, err := s.Store.ListDrafts(ctx)
	if err != nil {
		return nil, nil, pol, nil, fmt.Errorf("读取草稿: %w", err)
	}

	selected := map[string]bool{}
	for _, k := range resKeys {
		selected[k] = true
	}
	patches := map[string]json.RawMessage{}
	for _, d := range drafts {
		if selected[d.ResKey] {
			patches[d.ResKey] = d.Patch
		}
	}
	// used 记的是「这份草稿找到了它的底子」。没被记上的就是孤儿，见函数末尾。
	used := map[string]bool{}

	routes := make([]model.Route, 0, len(liveRoutes))
	for _, r := range liveRoutes {
		if p, ok := patches["route:"+r.Domain]; ok {
			used["route:"+r.Domain] = true
			merged, err := mergeInto(r, p)
			if err != nil {
				return nil, nil, pol, nil, fmt.Errorf("合并 %s 的草稿: %w", r.Domain, err)
			}
			r = merged
		}
		routes = append(routes, r)
	}

	rules := make([]model.Rule, 0, len(liveRules))
	for _, r := range liveRules {
		if p, ok := patches["rule:"+r.ID]; ok {
			used["rule:"+r.ID] = true
			secretPlain := r.Secret // 草稿里不会有密钥，合并不能把它弄丢
			merged, err := mergeRuleDraft(r, p)
			if err != nil {
				return nil, nil, pol, nil, fmt.Errorf("合并规则 %s 的草稿: %w", r.ID, err)
			}
			merged.Secret = secretPlain
			r = merged
		}
		rules = append(rules, r)
	}
	// 全局策略也吃草稿：工作台里它们与路由、规则共用同一套草稿机制。
	specs := map[string]json.RawMessage{}
	for _, p := range livePolicies {
		spec := p.Spec
		if patch, ok := patches["global:"+p.ID]; ok {
			used["global:"+p.ID] = true
			merged, err := mergeInto(p, patch)
			if err != nil {
				return nil, nil, pol, nil, fmt.Errorf("合并策略 %s 的草稿: %w", p.ID, err)
			}
			spec = merged.Spec
		}
		specs[p.ID] = spec
	}
	pol, err = render.ParsePolicies(specs[model.PolicyTLS], specs[model.PolicyLog])
	if err != nil {
		return nil, nil, pol, nil, err
	}

	// **勾了、而底下没有资源的草稿，在这里被点名。**
	//
	// 上面三个循环都是「遍历 live、有草稿就套上去」，所以一份没有 live 底子的
	// 草稿走完这一段之后，什么痕迹也不会留下。而调用方随后会删掉它。
	//
	// 报成校验问题（而不是自己造一个新的错误通道）是有意的：预览的
	// validation.ok 会转假、下发用 1002 拒绝执行——**而下发被拒，
	// DeleteDrafts 就跑不到，那份草稿因此活下来**。人还能把它改对。
	orphans := make([]render.Issue, 0)
	for _, k := range sortedKeys(patches) {
		if used[k] {
			continue
		}
		orphans = append(orphans, render.Issue{
			ResKey: k, Field: "res_key",
			Reason: "没有这个资源。草稿是「在已有资源上的改动」，没有底子合并不出东西；" +
				"要新建请先建出资源本身，再改它的草稿",
		})
	}

	return routes, rules, pol, orphans, nil
}

func sortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mergeInto 把 Partial 叠加到一个资源上。
//
// 走一次 JSON 往返而不是逐字段 if：字段会增加，逐字段合并的那个函数
// 每次都要跟着改，而漏掉一个字段的症状是「改了没生效」——最难排查的一类。
//
// # 嵌套对象要逐键叠加，不能整个替换
//
// **这一条是灰度上撞出来的。** 原先是浅合并（`m[k] = v`），于是草稿里的
// `spec` **整个替换掉**库里那份：
//
//	库里    spec = {requests: 300}
//	草稿    spec = {rate_key: "ip", window_s: 60}
//	合并后  spec = {rate_key: "ip", window_s: 60}   ← requests 没了
//
// 结果是「每窗口允许的请求数要大于 0」——而人在界面上从没碰过那个字段。
// **在工作台改一个字段，其余的会被静默丢掉**，任何有多个 spec 字段的
// 规则类型都中招。
//
// 而它同一个 bug 会报出不同的字段名（取决于草稿里当时缺哪个），
// 于是两轮排查都指向了「某个值被改成 0」，而真相是「字段整个消失了」。
//
// # 数组整体替换，不逐元素合并
//
// `ips: ["a"]` 覆盖 `ips: ["a","b"]` 必须是替换 —— 逐元素合并的话
// **删掉一个 IP 这件事永远表达不出来**。
func mergeInto[T any](base T, patch json.RawMessage) (T, error) {
	b, err := json.Marshal(base)
	if err != nil {
		return base, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return base, err
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(patch, &p); err != nil {
		return base, err
	}
	deepMerge(m, p)
	merged, err := json.Marshal(m)
	if err != nil {
		return base, err
	}
	var out T
	if err := json.Unmarshal(merged, &out); err != nil {
		return base, err
	}
	return out, nil
}

// deepMerge 把 src 叠加进 dst：两边都是对象时递归，否则整体覆盖。
func deepMerge(dst, src map[string]json.RawMessage) {
	for k, v := range src {
		cur, ok := dst[k]
		if !ok {
			dst[k] = v
			continue
		}
		var curObj, newObj map[string]json.RawMessage
		if json.Unmarshal(cur, &curObj) == nil && json.Unmarshal(v, &newObj) == nil {
			deepMerge(curObj, newObj)
			if b, err := json.Marshal(curObj); err == nil {
				dst[k] = b
				continue
			}
		}
		dst[k] = v
	}
}

// mergeRuleDraft 合并一条规则的草稿。
//
// **换规则类型时 spec 整体替换，不叠加。**
//
// 不这么做的话，把 ip_whitelist 改成 rate_limit 之后，深合并会让旧的 `ips`
// 留在新 spec 里 —— 渲染读不到它（spec 是判别联合），而它会出现在
// `GET /rules` 的响应里。界面照 type 决定显示哪些框，所以人看不见它，
// **而它会一直跟着这条规则走**，直到有人在某个响应里发现一个不该存在的字段。
func mergeRuleDraft(base model.Rule, patch json.RawMessage) (model.Rule, error) {
	var head struct {
		Type *string `json:"type"`
	}
	if err := json.Unmarshal(patch, &head); err != nil {
		return base, err
	}
	if head.Type != nil && *head.Type != base.Type {
		// 清空之后 base 的 spec 序列化成 {}，patch 的 spec 逐键落进去
		// —— 效果等同整体替换。
		base.Spec = model.RuleSpec{}
	}
	return mergeInto(base, patch)
}

// countEffectiveRules 只数真正生效的规则：未绑定域名的、停用的都不算。
// 心跳里那个数字要与节点上实际生效的一致，否则「这台机器上到底是哪一版」
// 这个问题会得到一个自相矛盾的答案。
func countEffectiveRules(rules []model.Rule) int {
	n := 0
	for _, r := range rules {
		if r.Enabled && len(r.ApplyTo) > 0 {
			n++
		}
	}
	return n
}

func keysWithPrefix(resKeys []string, prefix string) []string {
	var out []string
	for _, k := range resKeys {
		if id, ok := strings.CutPrefix(k, prefix); ok {
			out = append(out, id)
		}
	}
	return out
}

func (s *Scheduler) progress(deployID int64, cfgVersion, node, state, detail string, retrying bool) {
	if s.Hub == nil {
		return
	}
	s.Hub.Broadcast(ws.TypeDeployProgress, ws.DeployProgress{
		DeployID: deployID, CfgVersion: cfgVersion, Node: node,
		State: state, Detail: detail, Retrying: retrying,
	})
}

func (s *Scheduler) event(ctx context.Context, node, kind, msg string) {
	e, err := s.Store.InsertEvent(ctx, node, kind, msg)
	if err != nil {
		if s.Log != nil {
			s.Log.Error("写事件失败", "err", err)
		}
		return
	}
	if s.Hub != nil {
		s.Hub.Broadcast(ws.TypeEvent, ws.Event{
			ID: e.ID, At: e.CreatedAt.Format(time.RFC3339), Node: ws.NodeRef(e.Node), Kind: e.Kind, Msg: e.Msg,
		})
	}
}

// PreviewTarget 是预览里的一个目标节点及其当前状态。
type PreviewTarget struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Preview 是确认弹层的权威 diff 来源，同时是 dry-run。
//
// 返回**两份后端渲染的字节全文**，diff 在表示层算（ADR-0007 的补充与
// api-contract §7.1）。权威性来自「两份都是后端渲染的」，不来自谁算的 diff。
//
// 这里原先写的是「diff 由前端用自己的 LCS 算」——那是前端**当前的实现**，
// 而它换个算法这句就过期了，尽管后端一个字都不用改。
// **理由要压在有变更流程的东西上（契约、ADR），不要压在对方的当前实现上。**
type Preview struct {
	// Before / After 是指针，因为它们**可以没有**：
	//   - After 为 null = 校验没过，主控没有渲染出可下发的配置。
	//   - Before 为 null = 当前基线自己渲染不出来。
	//
	// 用 null 而不是空串（契约 §0.4）：空串在这里是一个**合法的配置内容**
	// （一份空配置），调用方分不出「没有」和「内容是空的」。一个按行 diff
	// 拿到空串会把整份配置渲染成全红删除——一个人填错了一个 IP，
	// 界面却告诉他「这次下发会删光所有配置」。那比不显示 diff 糟糕得多，
	// 而且出现在他最紧张的时刻。
	Before     *string         `json:"before"`
	After      *string         `json:"after"`
	Baseline   string          `json:"baseline"`
	Targets    []PreviewTarget `json:"targets"`
	Validation struct {
		OK     bool           `json:"ok"`
		Errors []render.Issue `json:"errors"`
	} `json:"validation"`
}

func (s *Scheduler) Preview(ctx context.Context, resKeys []string) (Preview, error) {
	var p Preview
	p.Validation.Errors = []render.Issue{}

	liveRoutes, err := s.Store.ListRoutes(ctx)
	if err != nil {
		return p, fmt.Errorf("读取路由: %w", err)
	}
	liveRules, err := s.Store.ListRules(ctx, s.Sealer)
	if err != nil {
		return p, fmt.Errorf("读取访问规则: %w", err)
	}
	afterRoutes, afterRules, afterPol, orphans, err := s.effective(ctx, resKeys)
	if err != nil {
		return p, err
	}
	// orphans 在下面与渲染问题合并——**不能在这里赋值**，
	// 那一段是 p.Validation.Errors = issues，会把这里写的整个盖掉。
	livePolicies, err := s.Store.ListPolicies(ctx)
	if err != nil {
		return p, fmt.Errorf("读取全局策略: %w", err)
	}
	var liveTLS, liveLog json.RawMessage
	for _, lp := range livePolicies {
		switch lp.ID {
		case model.PolicyTLS:
			liveTLS = lp.Spec
		case model.PolicyLog:
			liveLog = lp.Spec
		}
	}
	livePol, err := render.ParsePolicies(liveTLS, liveLog)
	if err != nil {
		return p, err
	}

	// **预览一律不带证书。** 于是 apps/tls 与 :443 那台 server 都不会出现在
	// diff 里——私钥不进浏览器（ADR-0007 补充），而 :443 的路由与 :80 完全相同，
	// 显示两遍只会让每一次路由改动在 diff 里翻倍，不增加任何信息。
	//
	// before 自己也可能渲染不出来，那不该让整个预览失败——前端拿到 null 时
	// 把整份显示为「全新增」即可，而 after 的校验结果仍然是有用的。
	if b, issues := render.Render(liveRoutes, liveRules, nil, livePol, s.Render); len(issues) == 0 {
		before := string(b)
		p.Before = &before
	}

	// **孤儿草稿与渲染问题合在一起报。** 两者对人是同一件事：
	// 「这次下发不会照你想的那样发生」，分成两个字段只会让界面多一条分支，
	// 而那条分支迟早有一边忘了显示。
	//
	// 注意顺序：孤儿在前。渲染问题说的是「这条资源哪里配错了」，
	// 而孤儿说的是「这条资源根本不存在」——后者是前者的前提，先看它。
	b, issues := render.Render(afterRoutes, afterRules, nil, afterPol, s.Render)
	p.Validation.Errors = append(append(p.Validation.Errors, orphans...), issues...)
	if len(p.Validation.Errors) > 0 {
		p.Validation.OK = false
	} else {
		p.Validation.OK = true
		after := string(b)
		p.After = &after
	}

	p.Baseline, err = s.Store.Baseline(ctx)
	if err != nil {
		return p, fmt.Errorf("读取基线: %w", err)
	}

	// 预览是 dry-run，不要求有在线节点——空数组是合法结果。
	for _, id := range s.Pusher.OnlineNodes() {
		p.Targets = append(p.Targets, PreviewTarget{ID: id, Status: "ok"})
	}
	if p.Targets == nil {
		p.Targets = []PreviewTarget{}
	}
	return p, nil
}

// RepushNode 把**当前基线那一版**重推给一个节点。
//
// 它不产生新版本，也不写下发记录：把一台掉队的机器带上来，不该在下发记录里
// 多出一次谁也没发起过的下发。推完那台机器的 cfg_version 就等于基线，
// 配置漂移随之消失。
//
// 这是 ADR-0005 的兜底：Caddy 拒绝的配置不自动重试，环境类临时故障
// 由人在这里手动恢复。
func (s *Scheduler) RepushNode(ctx context.Context, nodeID string) (string, string, []render.Issue, error) {
	baseline, err := s.Store.Baseline(ctx)
	if err != nil {
		return "", "", nil, fmt.Errorf("读取基线: %w", err)
	}
	if baseline == "" {
		return "", "", nil, ErrNoBaseline
	}

	routes, rules, pol, _, err := s.effective(ctx, nil) // 不带草稿：基线就是不含草稿的那一份（因此不会有孤儿）
	if err != nil {
		return "", "", nil, err
	}
	certs, err := s.certsForRender(ctx)
	if err != nil {
		return "", "", nil, err
	}
	cfg, issues := render.Render(routes, rules, certs, pol, s.Render)
	if len(issues) > 0 {
		return "", "", issues, nil
	}
	verifyRules, err := json.Marshal(render.VerifyRules(rules))
	if err != nil {
		return "", "", nil, fmt.Errorf("序列化校验规则: %w", err)
	}
	counts := tunnel.ResourceCounts{Routes: uint32(len(routes)), Rules: uint32(countEffectiveRules(rules))}

	out := s.Pusher.Push(ctx, nodeID, baseline, cfg, verifyRules, counts,
		s.upstreamCertFor(nodeID), PushDeadline)
	if !out.OK {
		s.event(ctx, nodeID, "warn", "重推失败："+out.Detail)
		return "", "", nil, fmt.Errorf("%s", out.Detail)
	}
	if err := s.Store.SetNodeCfgVersion(ctx, nodeID, baseline); err != nil {
		s.logger().Error("更新节点配置版本失败", "node", nodeID, "err", err)
	}
	s.event(ctx, nodeID, "ok", "已重推基线 "+baseline+"，耗时 "+out.Detail)
	return baseline, out.Detail, nil, nil
}

// NodeUp 补齐「接入」这个动作缺的两步：**把基线给它，把它加进解析。**
//
// 接入此前只回一个 cfg_version，不推配置也不动解析。而 Agent 拿到那个
// 版本号就记成自己的当前版本、心跳照它上报、主控又把它写回库 ——
// 新机器在界面上显示「与基线一致、无漂移」，而它的 Caddy 是空的、
// 解析里也没有它。**三方各自自洽，合起来是假的。**
//
// # 只对 fresh 做，而这个区分是承重的
//
// fresh 是凭 Token 的首次接入 —— 那台机器上的 Caddy 必然是空的。
// 而重连不同：Caddy 是独立的 systemd 服务，Agent 重启时它还带着配置在跑。
// 对每次重连都重推，一台在抖的机器会被反复推配置；对每次重连都同步解析，
// 它会反复打服务商的 API。**恢复那一路已经有人管了**（health 的
// recover → DNS.Attach），这里只管从来没有过的那一次。
//
// # 顺序：先配置，后解析
//
// 反过来的话，解析先指过去、而那台机器还什么都没有 —— 那是主动把
// 真实流量导向一台服务不了的机器。推失败就不碰解析：宁可它暂时不分流量，
// 也不要它分到流量却给不出响应。
func (s *Scheduler) NodeUp(ctx context.Context, nodeID string, fresh bool) {
	if !fresh {
		return
	}
	log := s.logger()

	pushed := false
	switch _, _, issues, err := s.RepushNode(ctx, nodeID); {
	case errors.Is(err, ErrNoBaseline):
		// **没有基线不是失败，是没东西可推。**
		// 这台机器与集群里其他机器一样就绪（大家都还没有配置），
		// 该照常进解析 —— 下面那次下发会把配置一起给它们。
	case err != nil:
		log.Error("新节点接入后推基线失败", "node", nodeID, "err", err)
		s.event(ctx, nodeID, "warn", fmt.Sprintf(
			"节点已接入，但推送基线配置失败：%v —— 这台机器上还没有配置，"+
				"解析也不会指向它。修好之后用「重推配置」", err))
		return
	case len(issues) > 0:
		log.Error("新节点接入后基线渲染不过", "node", nodeID, "issues", issues)
		s.event(ctx, nodeID, "warn",
			"节点已接入，但当前基线渲染不过，配置没能推下去；解析也不会指向它")
		return
	default:
		pushed = true
	}

	// **先问后做。** Sync 无论成败都会落一条同步状态，而没配服务商时
	// 那条记的是一件没发生过的事：界面上「从没同步过」是 at=null，
	// 一次接入会把它变成一个像模像样的时间戳 —— 空白会让人去查，
	// 一个看起来正常的时间不会。人点开关时该记（他问了，这是回答），
	// 一台机器接入是背景动作，不该记。
	attached := false
	if s.DNS != nil && s.DNS.Configured(ctx) {
		switch err := s.DNS.Attach(ctx, nodeID); {
		case err == nil:
			attached = true
		case errors.Is(err, dnsops.ErrNoProvider):
			// 问过之后配置又没了 —— 极窄的竞态，不算失败。
		default:
			// **说出来。** 不说的话，这台机器配置齐了、在线、界面上一切正常，
			// 而它一份流量也拿不到 —— 那看起来跟「刚装好还没起量」一样。
			log.Error("新节点接入后同步解析失败", "node", nodeID, "err", err)
			s.event(ctx, nodeID, "warn", fmt.Sprintf(
				"配置已下发到新节点，但同步解析失败：%v —— "+
					"它现在拿不到流量，去解析页看看", err))
			return
		}
	}

	// **措辞跟着实际发生的事走**，与 health 那边 recover() 同一条规矩：
	// 四种组合读起来不同，人接下来要做的也不同。
	//
	// 而这几句都**不能含「节点已接入」这个子串** —— 那是 store.EventNodeJoined
	// 的字面值，事件流里靠数它来分「接入」与「重连」。撞上的话，
	// 一次接入会被数成两次，而那正是那条计数要防的读法。
	switch {
	case pushed && attached:
		s.event(ctx, nodeID, "ok", "新节点已就绪：基线配置已下发，并已加入解析")
	case pushed:
		s.event(ctx, nodeID, "ok", "新节点已就绪：基线配置已下发（尚未配置 DNS 服务商，解析未变动）")
	case attached:
		s.event(ctx, nodeID, "ok", "新节点已加入解析；还没有基线，下一次下发会把配置给它")
	default:
		s.event(ctx, nodeID, "ok", "新节点就位；还没有基线，也尚未配置 DNS 服务商")
	}
}

// upstreamCertFor 为一个节点签一张 24 小时的回源客户端证书。
//
// **每次下发都重签**，而不是「快到期时才续」：叶子只有 24 小时，而下发的频率
// 远高于那个；顺手续上比另造一条轮换路径简单，也少一处会忘记跑的定时任务。
//
// 24 小时是刻意取短的（ADR-0009）：内部 PKI 的 CRL/OCSP 基本没人真部署，
// 写了也是摆设。叶子做短，吊销就退化成「停止续期」这一个动作。
// 代价是节点与主控失联超过 24 小时后回源 mTLS 失效——那是可接受的：
// 一台你整天联系不上的机器，不该继续拿着凭据进你的源站。
func (s *Scheduler) upstreamCertFor(nodeID string) tunnel.UpstreamCert {
	if s.UpstreamCA == nil || s.Render.UpstreamClientCert == "" {
		return tunnel.UpstreamCert{}
	}
	leaf, err := s.UpstreamCA.SignClient(nodeID, 24*time.Hour)
	if err != nil {
		s.logger().Error("签发回源证书失败", "node", nodeID, "err", err)
		return tunnel.UpstreamCert{}
	}
	return tunnel.UpstreamCert{
		CertPEM: leaf.CertPEM, KeyPEM: leaf.KeyPEM,
		CertPath: s.Render.UpstreamClientCert, KeyPath: s.Render.UpstreamClientKey,
	}
}

// certsForRender 取出要内联进配置的证书。
//
// 需要私钥明文——load_pem 就是把它内联进去（ADR-0010）。它只出现在下发的
// 载荷里，不经任何读接口回显，也不进快照与预览。
func (s *Scheduler) certsForRender(ctx context.Context) ([]render.Cert, error) {
	if s.Sealer == nil {
		// 没有密封器就取不出私钥。这时**不渲染证书**而不是报错：
		// 一个还没配密钥的系统应当能跑起来并下发 HTTP 配置。
		return nil, nil
	}
	list, err := s.Store.ListCerts(ctx, s.Sealer)
	if err != nil {
		return nil, fmt.Errorf("读取证书: %w", err)
	}
	out := make([]render.Cert, 0, len(list))
	for _, c := range list {
		if len(c.CertPEM) == 0 || len(c.KeyPEM) == 0 {
			continue
		}
		out = append(out, render.Cert{Domain: c.Domain, CertPEM: c.CertPEM, KeyPEM: c.KeyPEM})
	}
	return out, nil
}

// commit 把本次勾选的资源的**合并结果**写回 live。
//
// 只写本次勾选的：未勾选的草稿仍然是草稿，它们的值不该被顺手落地。
//
// **它不动 version，也不删草稿**——那两件由调用方分别做（BumpXVersions 与
// DeleteDrafts）。这里点明是因为「下发」在契约 §7.2 里是三件事，
// 而这个函数只做其中一件；一个只读到这里的人容易以为「合入 live」

// RollbackResult 是一次回滚写回了什么。
type RollbackResult struct {
	ResKeys []string  `json:"res_keys"`
	Skipped []Skipped `json:"skipped"`
}

// Rollback 把某一版的资源状态与当前 live 逐资源比对，差异**写回草稿**。
//
// **它不直接下发。** 人要在工作台看过 diff、确认之后走同一条流水线——
// 回滚不绕过校验，也同样留审计（PRD §6.3）。一个「点一下就把线上换掉」的
// 回滚按钮，和它要修复的那类事故是同一种性质。
func (s *Scheduler) Rollback(ctx context.Context, cfgVersion, operator string) (RollbackResult, error) {
	var out RollbackResult

	raw, err := s.Store.DeploySnapshot(ctx, cfgVersion)
	if err != nil {
		return out, err
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return out, fmt.Errorf("解析快照: %w", err)
	}

	liveRoutes, err := s.Store.ListRoutes(ctx)
	if err != nil {
		return out, fmt.Errorf("读取路由: %w", err)
	}
	liveRules, err := s.Store.ListRules(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("读取访问规则: %w", err)
	}

	patches, skipped, err := diffToDrafts(snap, liveRoutes, liveRules)
	if err != nil {
		return out, err
	}
	out.Skipped = skipped
	if out.Skipped == nil {
		out.Skipped = []Skipped{}
	}

	keys := make([]string, 0, len(patches))
	for k := range patches {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// **一次写进去，要么全在要么一条都不写。**
	//
	// 原先是逐条写、中途失败就地返回，而 out.ResKeys 还是 nil：工作台里亮着
	// 前几条、接口回 500、响应里一个 res_key 都不报。人接着按「待下发」发出去
	// 的是半个回滚（issue #44）。
	if err := s.Store.PutDrafts(ctx, patches, operator); err != nil {
		return out, err
	}
	out.ResKeys = keys

	s.event(ctx, "", "info",
		fmt.Sprintf("已把 %s 的差异写回草稿（%d 处），等待人工确认后下发", cfgVersion, len(keys)))
	return out, nil
}

// legacyVerifyKinds 是**不报 verify_kinds 的那些 Agent** 认得的类型。
//
// 空的 verify_kinds 表示旧 Agent（那个字段是后加的），**不是「一种都不认得」**。
// 这两个是最早就有的，而 rate_limit / geo_block 是后加的 —— 一台旧 Agent
// 碰到后两个会回 403。
var legacyVerifyKinds = map[string]bool{"service_secret": true, "jwt_bearer": true}

// unsupportedRules 找出「有节点认不出」的规则。
//
// **只看走校验端点的那几种。** ip_whitelist / ip_blacklist / request_filter
// 是 Caddy 原生匹配器渲染出来的，旧 Agent 照样应用得了 —— 它只是把一份
// Caddy 配置贴上去，不需要认识里面的任何东西。
func (s *Scheduler) unsupportedRules(ctx context.Context, rules []model.Rule) ([]render.Issue, error) {
	needs := map[string]bool{}
	for _, r := range rules {
		if !r.Enabled || len(r.ApplyTo) == 0 {
			continue
		}
		switch r.Type {
		case model.RuleServiceSecret, model.RuleJWTBearer,
			model.RuleRateLimit, model.RuleGeoBlock:
			needs[r.Type] = true
		}
	}
	if len(needs) == 0 {
		return nil, nil
	}

	nodes, err := s.Store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}

	var issues []render.Issue
	for _, r := range rules {
		if !r.Enabled || len(r.ApplyTo) == 0 || !needs[r.Type] {
			continue
		}
		var missing []string
		for _, n := range nodes {
			// **下线的节点不算。** 它收不到这次下发，而把它算进来会让人
			// 为了下发一条规则去把一台故意下线的机器升级掉。
			if n.DrainedAt != nil {
				continue
			}
			if !nodeSupports(n, r.Type) {
				missing = append(missing, n.ID)
			}
		}
		if len(missing) > 0 {
			issues = append(issues, render.Issue{
				ResKey: "rule:" + r.ID,
				Field:  "type",
				Reason: fmt.Sprintf(
					"这些节点上的 edge-agent 还不认识 %q 规则：%s。"+
						"照这样下发，那些节点上这条规则绑定的域名会对所有人返回 403 —— "+
						"先升级它们（edge-node.sh update --agent-bin <新二进制>）",
					r.Type, strings.Join(missing, "、")),
			})
		}
	}
	return issues, nil
}

func nodeSupports(n store.Node, kind string) bool {
	if len(n.VerifyKinds) == 0 {
		// 没报过 —— 旧 Agent，或者刚建还没连上过。
		// **按最保守的那一档处理**：认得的只有最早那两种。
		return legacyVerifyKinds[kind]
	}
	for _, k := range n.VerifyKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// commitFaultFn 把注入的错误包成一个**在事务中途**触发的钩子。
//
// 触发点在 live 已经合入、基线尚未确立的那一刻——那正是要验的状态。
// 放在整批开头的话一行都还没写，每次都会跳过被测的那个状态，
// 而测试照样是绿的（TestCommitIsAtomicAcrossRoutes 一开始就这么绿过一次）。
func (s *Scheduler) commitFaultFn() func() error {
	if s.commitFault == nil {
		return nil
	}
	return func() error { return s.commitFault }
}
