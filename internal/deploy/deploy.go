// Package deploy 编排一次配置下发。
//
// 流程：渲染 → 写快照 → 广播 → 收集逐节点结果 → 确立新基线。
//
// 主控不跑 caddy validate（docs/adr/0004）：渲染器在 Go 层拦下重复域名、空回源、
// 非法请求体上限等错误，而节点侧的 Caddy 本身就是原子拒绝——实测坏配置一律被
// 拒且原配置存活，一份坏配置打不挂节点。
package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// DefaultDeadline 是单个节点应用配置的超时（后端文档 §5 默认 5000ms）。
const DefaultDeadline = 5 * time.Second

// RetryPolicy 决定失败后重试几次、隔多久。
//
// **只对传输层失败生效**（docs/adr/0005）：节点没回应才重试。节点回应了但
// Caddy 拒绝配置时一次都不重试——同一份字节喂给同一个 Caddy 必然得到同一个
// 拒绝，重试只会在日志里刷 N 遍一模一样的报错，把真正的原因埋进噪声里。
type RetryPolicy struct {
	// Max 是总尝试次数（含首次）。
	Max int
	// Backoff 是首次重试前的等待，之后按 2 的幂增长。
	Backoff time.Duration
	// Deadline 是单次尝试等待节点回应的时长；为 0 时用 DefaultDeadline。
	Deadline time.Duration
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{Max: 5, Backoff: 500 * time.Millisecond, Deadline: DefaultDeadline}
}

func (p RetryPolicy) normalized() RetryPolicy {
	if p.Max <= 0 {
		p.Max = 1
	}
	if p.Deadline <= 0 {
		p.Deadline = DefaultDeadline
	}
	return p
}

// Store 是 deploy 需要的存储能力。
type Store interface {
	ListRoutes(ctx context.Context) ([]model.Route, error)
	CreateDeploy(ctx context.Context, d model.Deploy) (int64, error)
	PutDeployResult(ctx context.Context, r model.DeployResult) error
	BumpRouteVersions(ctx context.Context, domains []string) error
	ListDrafts(ctx context.Context) ([]model.Draft, error)
	DeleteDrafts(ctx context.Context, keys []string) error
}

// Tunnel 是 deploy 需要的下发通道能力。
type Tunnel interface {
	Connected() []string
	Send(nodeID string, msg *edgev1.MasterMsg) error
}

// Broadcaster 把下发进度推给控制台。
//
// 进度必须逐节点可见，不允许「整体成功/失败」的黑盒（PRD §7）。
type Broadcaster interface {
	Broadcast(f ws.Frame)
}

type Orchestrator struct {
	st    Store
	tun   Tunnel
	opts  render.Options
	retry RetryPolicy
	hub   Broadcaster
	log   *slog.Logger
	mu    sync.Mutex
	wait  map[string]chan *edgev1.PushResult // key: nodeID|cfgVersion
}

func New(st Store, tun Tunnel, log *slog.Logger) *Orchestrator {
	return NewWith(st, tun, render.DefaultOptions(), log)
}

// NewWith 指定渲染配置。监听地址是配置项而非常量——测试与本地调试跑在
// 非特权端口上，写死 :443 会让「能不能跑通」只能靠特权用户验证。
func NewWith(st Store, tun Tunnel, opts render.Options, log *slog.Logger) *Orchestrator {
	o := NewWithRetry(st, tun, DefaultRetryPolicy(), log)
	o.opts = opts
	return o
}

// NewWithRetry 指定重试策略。
func NewWithRetry(st Store, tun Tunnel, retry RetryPolicy, log *slog.Logger) *Orchestrator {
	if log == nil {
		log = slog.Default()
	}
	return &Orchestrator{
		st: st, tun: tun, opts: render.DefaultOptions(), retry: retry.normalized(),
		log: log, wait: map[string]chan *edgev1.PushResult{},
	}
}

// SetBroadcaster 装上进度广播。单独 setter 而非构造参数：hub 在装配顺序上
// 晚于编排器，且没有它时下发照样应当能跑（只是界面看不到实时进度）。
func (o *Orchestrator) SetBroadcaster(b Broadcaster) { o.hub = b }

func (o *Orchestrator) emit(t string, data map[string]any) {
	if o.hub != nil {
		o.hub.Broadcast(ws.Frame{Type: t, Data: data})
	}
}

// Result 是一次下发的整体结果。
type Result struct {
	DeployID   int64
	CfgVersion string
	Rows       []model.DeployResult
}

// Deploy 渲染当前配置并下发到所有在线节点。
//
// resKeys 是本次勾选的资源；为空表示全部。**只有勾选的草稿会被合入并清除**——
// 未勾选的原样留着。若下发顺手把全部草稿一起推了或顺手清空，别人还没推的改动
// 就被无声吞掉了，而他不会知道。
func (o *Orchestrator) Deploy(ctx context.Context, operator string, resKeys []string) (Result, error) {
	routes, err := o.st.ListRoutes(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("读取路由: %w", err)
	}
	payload, err := render.CaddyWith(routes, o.opts)
	if err != nil {
		// 渲染失败时一个节点都不触达——这正是不装 caddy 也能守住的那道线
		return Result{}, fmt.Errorf("渲染配置失败：%w", err)
	}

	targets := o.tun.Connected()
	if len(targets) == 0 {
		// 报告「成功推给 0 个节点」会让人以为配置已经生效。假成功比失败危险得多。
		return Result{}, fmt.Errorf("没有在线节点，本次下发未执行")
	}

	cfgVersion, err := newCfgVersion()
	if err != nil {
		return Result{}, err
	}
	var snapshot map[string]any
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return Result{}, fmt.Errorf("快照序列化: %w", err)
	}

	keys := resKeys
	if len(keys) == 0 {
		keys = resKeysOf(routes)
	}
	deployID, err := o.st.CreateDeploy(ctx, model.Deploy{
		CfgVersion: cfgVersion, Operator: operator,
		ResKeys: keys, Snapshot: snapshot, CreatedAt: time.Now(),
	})
	if err != nil {
		return Result{}, err
	}

	rows := o.broadcast(ctx, deployID, cfgVersion, payload, targets)

	// 至少有一个节点成功才清掉本次勾选的草稿。失败时留着，人改完能接着推。
	if anyOK(rows) && len(resKeys) > 0 {
		if err := o.st.DeleteDrafts(ctx, resKeys); err != nil {
			o.log.Error("清除草稿失败", "err", err)
		}
	}

	// 至少有一个节点成功才推进资源版本。版本号是「这条路由已经在节点上生效过」
	// 的唯一凭据：无一成功却推进，等于宣称已生效；成功了却不推进，界面永远显示
	// 「未下发」，用户会反复推同一份配置。
	if anyOK(rows) {
		if err := o.st.BumpRouteVersions(ctx, domainsOf(routes)); err != nil {
			o.log.Error("推进资源版本失败", "err", err)
		}
	}
	return Result{DeployID: deployID, CfgVersion: cfgVersion, Rows: rows}, nil
}

func (o *Orchestrator) broadcast(ctx context.Context, deployID int64, cfgVersion string,
	payload []byte, targets []string) []model.DeployResult {

	msg := &edgev1.MasterMsg{M: &edgev1.MasterMsg_Push{Push: &edgev1.PushConfig{
		CfgVersion: cfgVersion,
		CaddyJson:  payload,
		DeadlineMs: uint32(DefaultDeadline.Milliseconds()),
	}}}

	rows := make([]model.DeployResult, 0, len(targets))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, node := range targets {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			row := o.pushWithRetry(ctx, deployID, node, cfgVersion, msg)
			o.emit("deploy_progress", map[string]any{
				"deploy_id": deployID, "node": row.NodeID,
				"state": row.State, "res": row.Detail,
			})

			if err := o.st.PutDeployResult(ctx, row); err != nil {
				o.log.Error("写入下发结果失败", "node_id", node, "err", err)
			}
			mu.Lock()
			rows = append(rows, row)
			mu.Unlock()
		}(node)
	}
	wg.Wait()
	return rows
}

// pushWithRetry 向单个节点下发，按策略重试传输层失败。
func (o *Orchestrator) pushWithRetry(ctx context.Context, deployID int64, node, cfgVersion string,
	msg *edgev1.MasterMsg) model.DeployResult {

	row := model.DeployResult{DeployID: deployID, NodeID: node}
	backoff := o.retry.Backoff

	for attempt := 1; attempt <= o.retry.Max; attempt++ {
		// 登记必须在 Send **之前**：反过来的话，节点回报得足够快时结果会在
		// 登记完成前到达，于是没人接收，等待方一直等到超时——一次成功的下发
		// 被报成 deadline exceeded。
		o.emit("deploy_progress", map[string]any{
			"deploy_id": deployID, "node": node,
			"state": "pushing", "res": phaseText(attempt),
		})
		ch := o.register(node, cfgVersion)
		err := o.tun.Send(node, msg)

		if err == nil {
			select {
			case res := <-ch:
				o.unregister(node, cfgVersion)
				if res.GetOk() {
					row.State, row.Detail = "ok", res.GetDetail()
					return row
				}
				// 节点回应了，只是 Caddy 拒绝了配置——**不重试**（docs/adr/0005）。
				// 原文原样带走，排查时唯一有用的就是它。
				row.State, row.Detail = "fail", res.GetDetail()
				return row
			case <-time.After(o.retry.Deadline):
				row.State, row.Detail = "fail", "deadline exceeded"
			case <-ctx.Done():
				o.unregister(node, cfgVersion)
				row.State, row.Detail = "fail", "已取消"
				return row
			}
		} else {
			row.State, row.Detail = "fail", err.Error()
		}
		o.unregister(node, cfgVersion)

		if attempt < o.retry.Max {
			o.log.Warn("节点未回应，准备重试", "node_id", node, "attempt", attempt, "backoff", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				row.Detail = "已取消"
				return row
			}
			backoff *= 2
		}
	}
	return row
}

// OnPushResult 由隧道在收到节点回报时调用。
func (o *Orchestrator) OnPushResult(nodeID string, res *edgev1.PushResult) {
	o.mu.Lock()
	ch, ok := o.wait[key(nodeID, res.GetCfgVersion())]
	o.mu.Unlock()
	if !ok {
		// 迟到的回报（等待方已超时）没有归属，丢掉即可
		return
	}
	select {
	case ch <- res:
	default:
	}
}

// register 必须在 Send **之前**调用。
//
// 反过来的话，节点回报得足够快时结果会在登记完成前到达，于是没人接收，
// 等待方一直等到超时——一次成功的下发被报成 deadline exceeded。
func (o *Orchestrator) register(nodeID, cfgVersion string) chan *edgev1.PushResult {
	ch := make(chan *edgev1.PushResult, 1)
	o.mu.Lock()
	o.wait[key(nodeID, cfgVersion)] = ch
	o.mu.Unlock()
	return ch
}

func (o *Orchestrator) unregister(nodeID, cfgVersion string) {
	o.mu.Lock()
	delete(o.wait, key(nodeID, cfgVersion))
	o.mu.Unlock()
}

func key(nodeID, cfgVersion string) string { return nodeID + "|" + cfgVersion }

// phaseText 给进度帧一个人能看懂的阶段说明。
func phaseText(attempt int) string {
	if attempt == 1 {
		return "热重载中"
	}
	return fmt.Sprintf("重试中（第 %d 次）", attempt)
}

func anyOK(rows []model.DeployResult) bool {
	for _, r := range rows {
		if r.State == "ok" {
			return true
		}
	}
	return false
}

func domainsOf(routes []model.Route) []string {
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r.Domain)
	}
	return out
}

func resKeysOf(routes []model.Route) []string {
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, "route:"+r.Domain)
	}
	return out
}

func newCfgVersion() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成配置版本号: %w", err)
	}
	return "cfg-" + hex.EncodeToString(b[:]), nil
}
