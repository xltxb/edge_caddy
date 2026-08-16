// Package health 判定边缘节点的在线状态。
//
// 判定依据是心跳的新鲜度：连续错过 N 个心跳周期即判离线。判离线本身不做任何
// 补救动作（摘 DNS 属工单 #15），只更新状态并发一条事件。
package health

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// Store 是 health 需要的存储能力。
type Store interface {
	ListNodes(ctx context.Context) ([]model.Node, error)
	MarkNodeDown(ctx context.Context, id string) error
}

// Broadcaster 把状态变化推给控制台。
type Broadcaster interface {
	Broadcast(f ws.Frame)
}

type Config struct {
	// Interval 是心跳周期（后端文档 §7 默认 3s）。
	Interval time.Duration
	// MissedBeats 是判定离线所需的连续错过次数（默认 3）。
	MissedBeats int
	// Now 可替换时钟，仅供测试注入。
	Now    func() time.Time
	Logger *slog.Logger
}

type Checker struct {
	st  Store
	hub Broadcaster
	cfg Config
	log *slog.Logger

	// down 记录已经报过离线的节点。
	//
	// 没有它，巡检每 3 秒就会重复报一次同一个节点——一个掉了一夜的节点能在
	// 事件流里刷出上万条，把真正需要注意的东西全挤掉。
	mu   sync.Mutex
	down map[string]bool
}

func New(st Store, hub Broadcaster, cfg Config) *Checker {
	if cfg.Interval <= 0 {
		cfg.Interval = 3 * time.Second
	}
	if cfg.MissedBeats <= 0 {
		cfg.MissedBeats = 3
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Checker{st: st, hub: hub, cfg: cfg, log: cfg.Logger, down: map[string]bool{}}
}

// Run 周期性巡检，直到 ctx 结束。
func (c *Checker) Run(ctx context.Context) {
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Sweep(ctx)
		}
	}
}

// Sweep 跑一轮判定。
func (c *Checker) Sweep(ctx context.Context) {
	nodes, err := c.st.ListNodes(ctx)
	if err != nil {
		c.log.Error("巡检读取节点失败", "err", err)
		return
	}
	deadline := time.Duration(c.cfg.MissedBeats) * c.cfg.Interval
	now := c.cfg.Now()

	for _, n := range nodes {
		// 从未上报过心跳的节点是「还没接入」，不是「掉了」。混为一谈会让刚
		// 签发 Token 还没装的节点立刻触发一条离线告警。
		if n.LastHB.IsZero() {
			continue
		}
		stale := now.Sub(n.LastHB) > deadline

		c.mu.Lock()
		reported := c.down[n.ID]
		switch {
		case stale && !reported:
			c.down[n.ID] = true
			c.mu.Unlock()
			if err := c.st.MarkNodeDown(ctx, n.ID); err != nil {
				c.log.Error("标记节点离线失败", "node_id", n.ID, "err", err)
			}
			c.emit("crit", n.ID, "心跳连续超时，判定为离线")
			c.log.Warn("节点离线", "node_id", n.ID, "last_hb", n.LastHB)

		case !stale && reported:
			// 恢复后要把标记清掉，否则「掉线→恢复→再掉线」的第二次会静默
			delete(c.down, n.ID)
			c.mu.Unlock()
			c.emit("ok", n.ID, "心跳恢复，节点重新在线")
			c.log.Info("节点恢复", "node_id", n.ID)

		default:
			c.mu.Unlock()
		}
	}
}

func (c *Checker) emit(kind, nodeID, msg string) {
	if c.hub == nil {
		return
	}
	// 字段名与前端文档 §6 一致，前端直接用
	c.hub.Broadcast(ws.Frame{Type: "event", Data: map[string]any{
		"t":    c.cfg.Now().Format("15:04:05"),
		"node": nodeID,
		"kind": kind,
		"msg":  msg,
	}})
}
