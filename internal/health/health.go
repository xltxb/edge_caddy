// Package health 是心跳观测、离线判定与自愈。
//
// 「配置漂移」在这里只比对版本号，不回读节点上的配置内容
// （docs/adr/0002-drift-is-version-comparison.md）。这个局限必须在界面上说清楚：
// 一个叫「配置漂移」的指标，读者会理所当然地以为它能发现篡改。
package health

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/tunnel"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// cpuSeriesLen 是总览节点卡片上那条 sparkline 的点数。
const cpuSeriesLen = 12

// Sample 是一次心跳带来的观测量。
type Sample struct {
	CPU, Mem    float64
	Conns       uint32
	Routes      uint32
	Rules       uint32
	ReqTotal    uint64
	OriginTotal uint64
	At          time.Time
}

// nodeState 是单个节点在主控内存里的观测状态。
//
// **不落库**：心跳是纯粹易失的数据，离线判定用的是 last_hb_at 与连续超时计数，
// 不是这些序列。为一个只用来画 12 个点的东西建一张每天 17 万行的表不划算。
// 代价是主控重启后 sparkline 会空几十秒——前端按 null 处理（api-contract §4）。
type nodeState struct {
	cpu      []int
	last     Sample
	seen     bool
	misses   int
	downSent bool
	status   string

	// 上一轮的累计计数，用来算窗口内的回源率。
	prevReq, prevOrigin uint64
	hasPrev             bool
}

// Alerter 是告警的出口。装配在 #20 的 alert 包里。
type Alerter interface {
	Notify(ctx context.Context, level, title, body string)
}

// DNSDetacher 把一个节点摘出解析。由 dnsops.Orchestrator 实现。
type DNSDetacher interface {
	Detach(ctx context.Context, nodeID string) error
	Attach(ctx context.Context, nodeID string) error
}

// Config 是装配参数。与 Monitor 分开是因为 Monitor 带锁，
// 按值传一个含 sync.Mutex 的结构体会复制那把锁。
type Config struct {
	Store *store.Store
	Hub   *ws.Hub
	Log   *slog.Logger
	Alert Alerter
	DNS   DNSDetacher

	// Interval 与 Threshold 决定「多久没心跳算离线」。
	// 界面上那句「节点最长 N 秒后被摘除」= Interval × Threshold。
	Interval  time.Duration
	Threshold int

	// WarnCPUPct / WarnMemPct 决定「连着但不健康」的界线。
	// 为 0 时用默认值——不设阈值会让 warn 永远不被写入，
	// 而界面上「异常 N 个」那个桶就恒为 0。
	WarnCPUPct float64
	WarnMemPct float64
}

type Monitor struct {
	Config

	mu    sync.Mutex
	nodes map[string]*nodeState
}

func New(c Config) *Monitor {
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Interval <= 0 {
		c.Interval = 3 * time.Second
	}
	if c.Threshold <= 0 {
		c.Threshold = 3
	}
	if c.WarnCPUPct <= 0 {
		c.WarnCPUPct = 80
	}
	if c.WarnMemPct <= 0 {
		c.WarnMemPct = 90
	}
	return &Monitor{Config: c, nodes: map[string]*nodeState{}}
}

// Observe 记下一次心跳，返回它代表的健康分档（ok / warn）。
func (m *Monitor) Observe(hb tunnel.Heartbeat) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.nodes[hb.NodeID]
	if st == nil {
		st = &nodeState{}
		m.nodes[hb.NodeID] = st
	}

	st.cpu = append(st.cpu, int(hb.CPU+0.5))
	if len(st.cpu) > cpuSeriesLen {
		st.cpu = st.cpu[len(st.cpu)-cpuSeriesLen:]
	}
	st.last = Sample{
		CPU: hb.CPU, Mem: hb.Mem, Conns: hb.Conns,
		Routes: hb.Routes, Rules: hb.Rules,
		ReqTotal: hb.ReqTotal, OriginTotal: hb.OriginTotal,
		At: time.Now(),
	}
	st.seen = true
	st.misses = 0

	status := m.classify(hb)
	if status != st.status {
		st.status = status
		go m.announce(hb.NodeID, status, hb)
	}

	if st.downSent {
		st.downSent = false
		go m.recover(hb.NodeID)
	}
	return status
}

// Classify 判断一次心跳代表的健康状态。
//
// **`warn` 是「连着但不健康」**，不是「快离线了」。把这样一台机器算进「在线」，
// 会让 KPI 在一台 CPU 81%、内存快满的机器上仍然显示绿色——而巡检时最该被
// 看见的恰恰是那台。
func (m *Monitor) classify(hb tunnel.Heartbeat) string {
	if hb.CPU >= m.WarnCPUPct || hb.Mem >= m.WarnMemPct {
		return "warn"
	}
	return "ok"
}

// announce 在健康状态变化时写事件。只在**变化**时写，不是每个心跳都写——
// 一台持续高负载的机器会把事件流刷满，而那条流的价值在于「有事发生了」。
func (m *Monitor) announce(nodeID, status string, hb tunnel.Heartbeat) {
	ctx := context.Background()
	if status == "warn" {
		m.emit(ctx, nodeID, "warn",
			fmt.Sprintf("负载偏高：CPU %.1f%%，内存 %.1f%%", hb.CPU, hb.Mem))
		if m.Alert != nil {
			m.Alert.Notify(ctx, "warn", "节点负载偏高 "+nodeID,
				fmt.Sprintf("CPU %.1f%% / 内存 %.1f%%", hb.CPU, hb.Mem))
		}
		return
	}
	m.emit(ctx, nodeID, "ok", "负载已回落")
}

// CPUSeries 返回节点最近的 CPU 点。
//
// **没有数据时返回 nil**，调用方据此给出 JSON null 而不是一串 0——
// 0 会被读成「负载为零」，null 才说得出「没有数据」。
func (m *Monitor) CPUSeries(nodeID string) []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.nodes[nodeID]
	if st == nil || len(st.cpu) == 0 {
		return nil
	}
	return append([]int(nil), st.cpu...)
}

func (m *Monitor) Latest(nodeID string) (Sample, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.nodes[nodeID]
	if st == nil || !st.seen {
		return Sample{}, false
	}
	return st.last, true
}

// Forget 在节点下线时丢掉它的观测状态。
func (m *Monitor) Forget(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.nodes, nodeID)
}

// Run 跑离线判定循环，直到 ctx 结束。
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.tick(ctx)
		}
	}
}

// tick 数一遍谁没按时报到。
//
// 判定用「连续错过 N 个周期」而不是「距上次心跳超过 N×周期」：后者在主控刚启动、
// 还没收到任何心跳时会把全部节点判成离线，而它们可能一直好好的。
func (m *Monitor) tick(ctx context.Context) {
	deadline := m.Interval
	now := time.Now()

	m.mu.Lock()
	var down []string
	for id, st := range m.nodes {
		if !st.seen || st.downSent {
			continue
		}
		if now.Sub(st.last.At) < deadline {
			continue
		}
		st.misses++
		if st.misses >= m.Threshold {
			st.downSent = true
			down = append(down, id)
		}
	}
	m.mu.Unlock()

	for _, id := range down {
		m.markDown(ctx, id)
	}
}

func (m *Monitor) markDown(ctx context.Context, nodeID string) {
	m.Log.Warn("节点心跳超时，判定离线", "node", nodeID,
		"interval", m.Interval, "threshold", m.Threshold)

	if err := m.Store.SetNodeDown(ctx, nodeID); err != nil {
		m.Log.Error("标记节点离线失败", "node", nodeID, "err", err)
	}

	// **先摘解析再告警。** 反过来的话，人被叫醒时流量还在往一台死机器上打。
	detached := false
	if m.DNS != nil {
		if err := m.DNS.Detach(ctx, nodeID); err != nil {
			m.Log.Error("摘除解析失败", "node", nodeID, "err", err)
		} else {
			detached = true
		}
	}

	// 措辞必须与实际发生的事一致。没有配置 DNS 服务商时解析并没有被摘，
	// 说「已自动暂停解析」就是承诺一件没发生的事——和「ok 不等于已生效」
	// 是同一类问题。
	msg := "心跳连续超时 " + itoa(m.Threshold) + " 次，已判定离线"
	if detached {
		msg += "，并已暂停 DNS 解析"
	} else {
		msg += "；DNS 解析未变动（未配置服务商）"
	}
	m.emit(ctx, nodeID, "crit", msg)

	if m.Alert != nil {
		m.Alert.Notify(ctx, "crit", "节点离线 "+nodeID, msg)
	}
}

func (m *Monitor) recover(nodeID string) {
	ctx := context.Background()
	m.Log.Info("节点心跳恢复", "node", nodeID)

	if m.DNS != nil {
		if err := m.DNS.Attach(ctx, nodeID); err != nil {
			m.Log.Error("恢复解析失败", "node", nodeID, "err", err)
		}
	}
	m.emit(ctx, nodeID, "ok", "心跳已恢复")
	if m.Alert != nil {
		m.Alert.Notify(ctx, "warn", "节点恢复 "+nodeID, "心跳已恢复")
	}
}

func (m *Monitor) emit(ctx context.Context, node, kind, msg string) {
	e, err := m.Store.InsertEvent(ctx, node, kind, msg)
	if err != nil {
		m.Log.Error("写事件失败", "err", err)
		return
	}
	if m.Hub != nil {
		m.Hub.Broadcast(ws.TypeEvent, ws.Event{
			ID: e.ID, At: e.CreatedAt.Format(time.RFC3339),
			Node: ws.NodeRef(e.Node), Kind: e.Kind, Msg: e.Msg,
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
