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
	// **先把库里已知的节点装进内存，再开始数。**
	//
	// 放在 Run 里而不是让 main.go 调：装配漏了的话，症状是
	// 「一台死了很久的机器永远显示在线」——那是最不该靠人记得的一步。
	m.warm(ctx)

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

// warm 把库里已知的节点装进内存，让 sweep 从主控启动那一刻就覆盖它们。
//
// **这一步此前不存在，而它的缺席造出了一个永久的漏报。**
//
// m.nodes 只在**收到心跳时**才建条目（见 Observe）。所以主控重启之后，
// 一台此时已经失联的机器再也不会发心跳，就永远进不了这张 map——
// tick 遍历不到它，SetNodeDown 一次也不会被调用，库里的 status 就此
// 永久停在 ok。灰度上撞到了：一台 31 分钟没心跳的机器，控制台显示「在线」。
//
// 连带的两件事更贵：SetNodeDown 同时负责**摘解析**和**发离线告警**，
// 它没跑，那台死机器就还挂在解析里，而且没有人被通知。
//
// **而制造这个洞的，正是 tick 上面那段防误报的注释。**
// 「数连续错过的次数，而不是看距上次心跳多久」——那个顾虑是对的
// （主控刚启动时不该把所有节点判成离线），但「数次数」只对已经在 map 里的
// 节点数。**一道防误报的措施，造出了一个永久的漏报。**
//
// 装进来之后那个顾虑仍然被照顾着：新装进来的条目 misses 从 0 开始，
// 一台真死的机器要连续错过 Threshold 个周期才被标记——
// 与一台在主控运行期间死掉的机器走的是同一条路径、同样的延迟。
func (m *Monitor) warm(ctx context.Context) {
	if m.Store == nil {
		return
	}
	nodes, err := m.Store.ListNodes(ctx)
	if err != nil {
		// 装不进来就退回原来的行为（只覆盖发过心跳的节点）。
		// **说出来**：这不是「没有节点」，是「不知道有没有节点」。
		m.Log.Error("装载已知节点失败，离线判定这一轮只覆盖发过心跳的节点",
			"err", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var n int
	for _, node := range nodes {
		if _, ok := m.nodes[node.ID]; ok {
			continue // 已经有心跳进来了，那份状态更新
		}
		// **被人下线的不装。** 它必然停止心跳，而那是我们主动做的事
		// （ADR-0014）；markDown 也会挡住它，不装只是省掉一轮无谓的计数。
		if node.DrainedAt != nil {
			continue
		}
		st := &nodeState{seen: true, status: node.Status}
		if node.LastHBAt != nil {
			st.last.At = *node.LastHBAt
		}
		// **库里已经是 down 的，标成「已报过」。**
		//
		// 不标的话，每一次主控重启都会为同一台死机器重新走一遍 markDown：
		// 再发一次告警、再摘一次解析。而一个重启就重复报警的系统，
		// 会教会运维忽略那一类告警——跟「下线的节点不报离线」是同一条理由。
		if node.Status == store.StatusDown {
			st.downSent = true
		}
		m.nodes[node.ID] = st
		n++
	}
	if n > 0 {
		m.Log.Info("已装载已知节点，离线判定从现在起覆盖它们", "count", n)
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
	// **被人下线的节点不报离线。** 它必然停止心跳——主控断了它的隧道并拒绝
	// 它重连——所以离线判定一定会命中，而它命中的是一件我们主动做的事
	// （ADR-0014）。
	//
	// 一个下线之后仍然每分钟报警的系统，会教会运维忽略那一类告警，
	// 而下次真有机器挂了的时候他们照旧会忽略。
	//
	// 查库放在这里而不是 sweep 的循环里：sweep 每个周期都跑，
	// 这条路径只在真要标记的那一次走到。
	drained, err := m.Store.IsNodeDrained(ctx, nodeID)
	if err != nil {
		// 查不出来就按「没下线」办：漏报一次真故障，比因为一次数据库抖动
		// 把告警吞掉要好。
		m.Log.Error("查下线状态失败，按未下线处理", "node", nodeID, "err", err)
	} else if drained {
		m.Log.Info("节点已被下线，心跳停止是预期的，不报离线", "node", nodeID)
		return
	}

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

	// **先把标志位放回去，再推服务商。顺序不能反。**
	//
	// dnsops.Attach 只负责「让服务商侧跟上」，它读的是库里的标志位——
	// 标志位还是 false 时推过去，推的是一份**不含这台机器**的安排。
	//
	// 而这一步此前根本不存在：摘的时候 SetNodeDown 在 SQL 里直接置 false，
	// 恢复的时候只调 Attach。**摘写库、恢复不写库**，于是标志位一旦被
	// 自动摘掉就再也回不来。灰度上的形态是：主控每重启一次，
	// 所有节点都被自动摘掉（重启窗口里它们必然错过几个心跳），而重启是例行操作。
	reattached := false
	if r, err := m.Store.ReattachAfterRecovery(ctx, nodeID); err != nil {
		m.Log.Error("恢复解析标志位失败", "node", nodeID, "err", err)
	} else {
		reattached = r
	}

	synced := false
	if m.DNS != nil {
		if err := m.DNS.Attach(ctx, nodeID); err != nil {
			m.Log.Error("恢复解析失败", "node", nodeID, "err", err)
		} else {
			synced = true
		}
	}

	// **措辞必须与实际发生的事一致**，跟 markDown 那边同一条规矩。
	//
	// 三种情况读起来完全不同，而人接下来的动作也不同：
	//
	//	放回去了、也推上去了 → 什么都不用做
	//	放回去了、没推上去   → 库里对了而服务商上没有，要去看服务商
	//	没放回去             → 这台机器的解析是**人**关的，系统不会替他开
	msg := "心跳已恢复"
	switch {
	case reattached && synced:
		msg += "，解析已恢复"
	case reattached:
		msg += "，解析标志位已恢复，但没能同步到服务商"
	default:
		// 没放回去只有一种来路：它不是系统摘的。人关的解析要人自己开，
		// 一次心跳抖动不该撤销人的决定。
		msg += "；解析仍是暂停状态（不是系统摘的，要人手动恢复）"
	}
	m.emit(ctx, nodeID, "ok", msg)
	if m.Alert != nil {
		m.Alert.Notify(ctx, "warn", "节点恢复 "+nodeID, msg)
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
