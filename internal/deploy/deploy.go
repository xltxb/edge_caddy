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
)

// DefaultDeadline 是单个节点应用配置的超时（后端文档 §5 默认 5000ms）。
const DefaultDeadline = 5 * time.Second

// Store 是 deploy 需要的存储能力。
type Store interface {
	ListRoutes(ctx context.Context) ([]model.Route, error)
	CreateDeploy(ctx context.Context, d model.Deploy) (int64, error)
	PutDeployResult(ctx context.Context, r model.DeployResult) error
}

// Tunnel 是 deploy 需要的下发通道能力。
type Tunnel interface {
	Connected() []string
	Send(nodeID string, msg *edgev1.MasterMsg) error
}

type Orchestrator struct {
	st   Store
	tun  Tunnel
	opts render.Options
	log  *slog.Logger
	mu   sync.Mutex
	wait map[string]chan *edgev1.PushResult // key: nodeID|cfgVersion
}

func New(st Store, tun Tunnel, log *slog.Logger) *Orchestrator {
	return NewWith(st, tun, render.DefaultOptions(), log)
}

// NewWith 指定渲染配置。监听地址是配置项而非常量——测试与本地调试跑在
// 非特权端口上，写死 :443 会让「能不能跑通」只能靠特权用户验证。
func NewWith(st Store, tun Tunnel, opts render.Options, log *slog.Logger) *Orchestrator {
	if log == nil {
		log = slog.Default()
	}
	return &Orchestrator{st: st, tun: tun, opts: opts, log: log, wait: map[string]chan *edgev1.PushResult{}}
}

// Result 是一次下发的整体结果。
type Result struct {
	DeployID   int64
	CfgVersion string
	Rows       []model.DeployResult
}

// Deploy 渲染当前配置并下发到所有在线节点。
func (o *Orchestrator) Deploy(ctx context.Context, operator string) (Result, error) {
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

	deployID, err := o.st.CreateDeploy(ctx, model.Deploy{
		CfgVersion: cfgVersion, Operator: operator,
		ResKeys: resKeysOf(routes), Snapshot: snapshot, CreatedAt: time.Now(),
	})
	if err != nil {
		return Result{}, err
	}

	rows := o.broadcast(ctx, deployID, cfgVersion, payload, targets)
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
			row := model.DeployResult{DeployID: deployID, NodeID: node}

			ch := o.register(node, cfgVersion)
			defer o.unregister(node, cfgVersion)

			if err := o.tun.Send(node, msg); err != nil {
				row.State, row.Detail = "fail", err.Error()
			} else {
				select {
				case res := <-ch:
					if res.GetOk() {
						row.State, row.Detail = "ok", res.GetDetail()
					} else {
						row.State, row.Detail = "fail", res.GetDetail()
					}
				case <-time.After(DefaultDeadline):
					row.State, row.Detail = "fail", "deadline exceeded"
				case <-ctx.Done():
					row.State, row.Detail = "fail", "已取消"
				}
			}

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
