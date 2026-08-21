// Package deploy 是下发流水线：草稿 → 校验 → 广播 → 逐节点回报 → 确立新基线。
//
// 「下发」是把选中的草稿合入基线并广播到各边缘节点的完整过程（CONTEXT.md）。
// 一次下发只携带**本次勾选**的草稿，未勾选的仍是草稿。
package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

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

	// EnsureCerts 在下发后为路由域名确保证书存在。
	//
	// 证书跟着路由走：人配了一个域名就该有证书，不该还要手动点一次签发。
	// 做成回调是为了不让下发依赖证书包（那会成环）。
	EnsureCerts func(ctx context.Context, domains []string)

	// RetryBackoff 是第一次重试前的等待，此后翻倍。留空即用默认的 1 秒。
	// 做成字段只为让重试策略能被单独测——真跑 1+2+4+8+16 秒的测试不会有人跑。
	RetryBackoff time.Duration

	retryOnce sync.Once
	retrier   *Retrier
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
	log := s.Log
	if log == nil {
		log = slog.Default()
	}

	routes, rules, err := s.effective(ctx, resKeys)
	if err != nil {
		return Result{}, nil, err
	}

	certs, err := s.certsForRender(ctx)
	if err != nil {
		return Result{}, nil, err
	}

	cfg, issues := render.Render(routes, rules, certs, s.Render)
	if len(issues) > 0 {
		// 校验不过即整体拒绝，不触达节点。
		return Result{}, issues, nil
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
		if err := s.commit(ctx, resKeys, routes, rules); err != nil {
			log.Error("把下发内容合入基线失败", "err", err)
		}

		// 至少有一台真的应用了，基线才前进。全部失败时什么都没变，
		// 让基线前进会让「配置漂移」把所有节点都算成漂移，而真相是没人变过。
		if err := s.Store.SetBaseline(ctx, cfgVersion, deployID); err != nil {
			log.Error("确立基线失败", "err", err)
		}
		if err := s.Store.BumpRouteVersions(ctx, keysWithPrefix(resKeys, "route:")); err != nil {
			log.Error("推进路由版本失败", "err", err)
		}
		if err := s.Store.BumpRuleVersions(ctx, keysWithPrefix(resKeys, "rule:")); err != nil {
			log.Error("推进规则版本失败", "err", err)
		}
		if err := s.Store.DeleteDrafts(ctx, resKeys); err != nil {
			log.Error("清空草稿失败", "err", err)
		}
	}

	// 首轮跑完就返回，掉队的交给后台补推：5 次指数退避最长要一分多钟，
	// 而 PRD 要求单次全网推送 6 节点 ≤10s 完成反馈。进度继续经 WS 推。
	s.Retries().enqueue(retryJob{
		deployID: deployID, cfgVersion: cfgVersion,
		caddyJSON: cfg, verifyRules: verifyRules, counts: counts, nodes: needRetry,
	})

	if s.EnsureCerts != nil {
		domains := make([]string, 0, len(routes))
		for _, r := range routes {
			domains = append(domains, r.Domain)
		}
		// 异步：ACME 要跟服务商往返，同步等会把下发拖很久。
		go s.EnsureCerts(context.WithoutCancel(ctx), domains)
	}

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
func (s *Scheduler) effective(ctx context.Context, resKeys []string) ([]model.Route, []model.Rule, error) {
	liveRoutes, err := s.Store.ListRoutes(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("读取路由: %w", err)
	}
	// 渲染需要共享密钥的明文：校验端点要拿它验签。它只出现在下发的载荷里，
	// 不经任何读接口回显。
	liveRules, err := s.Store.ListRules(ctx, s.Sealer)
	if err != nil {
		return nil, nil, fmt.Errorf("读取访问规则: %w", err)
	}
	drafts, err := s.Store.ListDrafts(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("读取草稿: %w", err)
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

	routes := make([]model.Route, 0, len(liveRoutes))
	for _, r := range liveRoutes {
		if p, ok := patches["route:"+r.Domain]; ok {
			merged, err := mergeInto(r, p)
			if err != nil {
				return nil, nil, fmt.Errorf("合并 %s 的草稿: %w", r.Domain, err)
			}
			r = merged
		}
		routes = append(routes, r)
	}

	rules := make([]model.Rule, 0, len(liveRules))
	for _, r := range liveRules {
		if p, ok := patches["rule:"+r.ID]; ok {
			secretPlain := r.Secret // 草稿里不会有密钥，合并不能把它弄丢
			merged, err := mergeInto(r, p)
			if err != nil {
				return nil, nil, fmt.Errorf("合并规则 %s 的草稿: %w", r.ID, err)
			}
			merged.Secret = secretPlain
			r = merged
		}
		rules = append(rules, r)
	}
	return routes, rules, nil
}

// mergeInto 把 Partial 叠加到一个资源上。
//
// 走一次 JSON 往返而不是逐字段 if：字段会增加，逐字段合并的那个函数
// 每次都要跟着改，而漏掉一个字段的症状是「改了没生效」——最难排查的一类。
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
	for k, v := range p {
		m[k] = v
	}
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
// 返回**两份后端渲染的字节全文**，diff 由前端用自己的 LCS 算。
// 权威性来自「两份都是后端渲染的」，不来自谁算的 diff——那样全站只有一套 diff
// 实现，右栏的可读表示与弹层的权威 diff 复用同一个折叠交互
// （ADR-0007 的补充与 api-contract §7.1）。
type Preview struct {
	// Before / After 是指针，因为它们**可以没有**：
	//   - After 为 null = 校验没过，主控没有渲染出可下发的配置。
	//   - Before 为 null = 当前基线自己渲染不出来。
	//
	// 用 null 而不是空串（契约 §0.4）：空串在这里是一个**合法的配置内容**
	// （一份空配置），调用方分不出「没有」和「内容是空的」。前端的 diff 组件
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
	afterRoutes, afterRules, err := s.effective(ctx, resKeys)
	if err != nil {
		return p, err
	}

	// **预览一律不带证书。** 于是 apps/tls 与 :443 那台 server 都不会出现在
	// diff 里——私钥不进浏览器（ADR-0007 补充），而 :443 的路由与 :80 完全相同，
	// 显示两遍只会让每一次路由改动在 diff 里翻倍，不增加任何信息。
	//
	// before 自己也可能渲染不出来，那不该让整个预览失败——前端拿到 null 时
	// 把整份显示为「全新增」即可，而 after 的校验结果仍然是有用的。
	if b, issues := render.Render(liveRoutes, liveRules, nil, s.Render); len(issues) == 0 {
		before := string(b)
		p.Before = &before
	}

	if b, issues := render.Render(afterRoutes, afterRules, nil, s.Render); len(issues) > 0 {
		p.Validation.OK = false
		p.Validation.Errors = issues
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
		return "", "", nil, fmt.Errorf("还没有基线，先完成一次下发")
	}

	routes, rules, err := s.effective(ctx, nil) // 不带草稿：基线就是不含草稿的那一份
	if err != nil {
		return "", "", nil, err
	}
	certs, err := s.certsForRender(ctx)
	if err != nil {
		return "", "", nil, err
	}
	cfg, issues := render.Render(routes, rules, certs, s.Render)
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
func (s *Scheduler) commit(ctx context.Context, resKeys []string, routes []model.Route, rules []model.Rule) error {
	selected := map[string]bool{}
	for _, k := range resKeys {
		selected[k] = true
	}
	for _, r := range routes {
		if selected["route:"+r.Domain] {
			if err := s.Store.UpsertRoute(ctx, r); err != nil {
				return fmt.Errorf("合入路由 %s: %w", r.Domain, err)
			}
		}
	}
	for _, r := range rules {
		if selected["rule:"+r.ID] {
			// 密钥传空串表示保持不变——它不在草稿里，也不该被这一步碰。
			if err := s.Store.UpsertRule(ctx, r, "", s.Sealer); err != nil {
				return fmt.Errorf("合入规则 %s: %w", r.ID, err)
			}
		}
	}
	return nil
}

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

	for _, k := range keys {
		if err := s.Store.PutDraft(ctx, k, patches[k], operator); err != nil {
			return out, fmt.Errorf("写回草稿 %s: %w", k, err)
		}
	}
	out.ResKeys = keys

	s.event(ctx, "", "info",
		fmt.Sprintf("已把 %s 的差异写回草稿（%d 处），等待人工确认后下发", cfgVersion, len(keys)))
	return out, nil
}
