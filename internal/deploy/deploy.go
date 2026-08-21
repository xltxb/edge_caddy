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
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/tunnel"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// PushDeadline 是单个节点的热重载超时。超过它算传输层失败（ADR-0005）。
const PushDeadline = 5 * time.Second

// Pusher 是隧道在这一层的最小面貌。抽出来只为让下发能被单独测。
type Pusher interface {
	OnlineNodes() []string
	Push(ctx context.Context, nodeID, cfgVersion string, caddyJSON []byte, deadline time.Duration) tunnel.PushOutcome
}

type Scheduler struct {
	Store  *store.Store
	Pusher Pusher
	Hub    *ws.Hub
	Log    *slog.Logger
	Render render.Options
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

	routes, err := s.effectiveRoutes(ctx, resKeys)
	if err != nil {
		return Result{}, nil, err
	}

	cfg, issues := render.Render(routes, s.Render)
	if len(issues) > 0 {
		// 校验不过即整体拒绝，不触达节点。
		return Result{}, issues, nil
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
			out := s.Pusher.Push(ctx, node, cfgVersion, cfg, PushDeadline)
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
		if err := s.Store.BumpRouteVersions(ctx, domainsOf(resKeys)); err != nil {
			log.Error("推进资源版本失败", "err", err)
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

// effectiveRoutes = 基线 + **本次勾选**的草稿。未勾选的草稿不参与本次渲染。
func (s *Scheduler) effectiveRoutes(ctx context.Context, resKeys []string) ([]model.Route, error) {
	live, err := s.Store.ListRoutes(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取路由: %w", err)
	}
	drafts, err := s.Store.ListDrafts(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取草稿: %w", err)
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

	out := make([]model.Route, 0, len(live))
	for _, r := range live {
		if p, ok := patches["route:"+r.Domain]; ok {
			merged, err := mergeRoute(r, p)
			if err != nil {
				return nil, fmt.Errorf("合并 %s 的草稿: %w", r.Domain, err)
			}
			r = merged
		}
		out = append(out, r)
	}
	return out, nil
}

// mergeRoute 把 Partial 叠加到一条路由上。
//
// 走一次 JSON 往返而不是逐字段 if：字段会增加，逐字段合并的那个函数
// 每次都要跟着改，而漏掉一个字段的症状是「改了没生效」——最难排查的一类。
func mergeRoute(base model.Route, patch json.RawMessage) (model.Route, error) {
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
	var out model.Route
	if err := json.Unmarshal(merged, &out); err != nil {
		return base, err
	}
	return out, nil
}

func domainsOf(resKeys []string) []string {
	var out []string
	for _, k := range resKeys {
		if d, ok := strings.CutPrefix(k, "route:"); ok {
			out = append(out, d)
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
