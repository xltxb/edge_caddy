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
	"strings"
	"sync"
	"time"

	"github.com/xltxb/edge_caddy/internal/model"
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
	Push(ctx context.Context, nodeID, cfgVersion string, caddyJSON, verifyRules []byte, deadline time.Duration) tunnel.PushOutcome
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

	cfg, issues := render.Render(routes, rules, s.Render)
	if len(issues) > 0 {
		// 校验不过即整体拒绝，不触达节点。
		return Result{}, issues, nil
	}

	// 验签材料走旁路，不进 Caddy 配置——Admin API 能读回整份运行配置。
	verifyRules, err := json.Marshal(render.VerifyRules(rules))
	if err != nil {
		return Result{}, nil, fmt.Errorf("序列化校验规则: %w", err)
	}

	targets := s.Pusher.OnlineNodes()
	if len(targets) == 0 {
		return Result{}, nil, ErrNoOnlineNodes
	}

	cfgVersion := store.NewCfgVersion()
	deployID, err := s.Store.CreateDeploy(ctx, cfgVersion, operator, resKeys, cfg, targets)
	if err != nil {
		return Result{}, nil, fmt.Errorf("写入下发记录: %w", err)
	}

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
			out := s.Pusher.Push(ctx, node, cfgVersion, cfg, verifyRules, PushDeadline)
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
			// 本切片不实现重试队列（属于 #19）。没有队列却报 retrying=true，
			// 是在界面上承诺一件不会发生的事。
			const retrying = false
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
	for _, r := range results {
		if r.out.OK {
			okCount++
			if err := s.Store.SetNodeCfgVersion(ctx, r.node, cfgVersion); err != nil {
				log.Error("更新节点配置版本失败", "node", r.node, "err", err)
			}
		} else {
			failCount++
		}
	}

	if err := s.Store.FinishDeploy(ctx, deployID, okCount, failCount); err != nil {
		log.Error("收尾下发记录失败", "err", err)
	}

	if okCount > 0 {
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

	s.event(ctx, "", eventKind(okCount, failCount),
		fmt.Sprintf("配置 %s 下发完成，%d/%d 节点", cfgVersion, okCount, len(targets)))

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
			ID: e.ID, At: e.CreatedAt.Format(time.RFC3339), Node: e.Node, Kind: e.Kind, Msg: e.Msg,
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

	// before 是当前基线所代表的内容。它自己也可能渲染不出来（比如某条路由的
	// mtls 还开着），那不该让整个预览失败——前端拿到 null 时把整份显示为
	// 「全新增」即可，而 after 的校验结果仍然是有用的。
	if b, issues := render.Render(liveRoutes, liveRules, s.Render); len(issues) == 0 {
		before := string(b)
		p.Before = &before
	}

	if b, issues := render.Render(afterRoutes, afterRules, s.Render); len(issues) > 0 {
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
