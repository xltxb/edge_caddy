package health

import (
	"context"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

// TestTickFasterThanHeartbeatDoesNotFlap：判定周期比节点的心跳间隔短时，不会把每个节点判 down（#82）。
//
// heartbeat_interval_s 只到得了主控这一侧，节点照旧每 3 秒报一次到。
// 此前 tick 拿判定周期当「这一周期有没有心跳」的线：设成 1 秒、阈值 2，
// 两个心跳之间就攒满了，全体节点同时被判 down、摘解析，下一个心跳又全部恢复。
func TestTickFasterThanHeartbeatDoesNotFlap(t *testing.T) {
	const id = "node-hk-01"
	m := New(Config{Interval: time.Second, Threshold: 2})
	seed(m, id)
	t0 := time.Now().Add(-time.Minute)
	for i := range 4 {
		m.observeAt(hbFor(id), t0.Add(time.Duration(i)*3*time.Second))
	}
	last := t0.Add(9 * time.Second)

	// 一个心跳周期里的 tick，下一个心跳晚到 200ms（寻常的抖动）。
	for _, age := range []time.Duration{
		time.Second, 2 * time.Second, 3200 * time.Millisecond,
	} {
		m.tickAt(t.Context(), last.Add(age))
	}
	if m.isDown(id) {
		t.Fatal("节点每 3 秒一次心跳从没断过，tick 跑得比它快就把它判 down 了")
	}
}

// TestSlowHeartbeatStillGoesDown：放宽的只是「一周期」的那条线，真死的机器照样会被判 down，
// 而且 StaleAfter 盖得住判 down 那一刻样本的年龄。
func TestSlowHeartbeatStillGoesDown(t *testing.T) {
	const id = "node-hk-01"
	const interval, threshold = time.Second, 2
	// 真库：走到判 down 那一步会 markDown（摘解析、写状态、发告警）。
	st := testdb.New(t)
	if err := st.UpsertNode(context.Background(), store.NodeSpec{
		NodeID: id, City: "香港", Vendor: "v", Line: "l", PublicIP: "203.0.113.7",
	}); err != nil {
		t.Fatal(err)
	}
	m := New(Config{Store: st, Interval: interval, Threshold: threshold})
	seed(m, id)
	t0 := time.Now().Add(-time.Minute)
	for i := range 4 {
		m.observeAt(hbFor(id), t0.Add(time.Duration(i)*3*time.Second))
	}
	last := t0.Add(9 * time.Second)

	// 最坏相位：第一次越线的 tick 落在样本刚满 3 秒之后差一点点。
	gap := 3 * time.Second
	phase := gap - time.Millisecond
	m.tickAt(t.Context(), last.Add(phase))
	var downAt time.Duration
	for k := 1; k <= threshold+2 && downAt == 0; k++ {
		age := phase + time.Duration(k)*interval
		m.tickAt(t.Context(), last.Add(age))
		if m.isDown(id) {
			downAt = age
		}
	}
	if downAt == 0 {
		t.Fatal("心跳停了这么久还没判 down —— 放宽判据把真故障一起吃掉了")
	}
	if got := m.StaleAfter(); got < downAt || got > downAt+interval {
		t.Fatalf("StaleAfter = %v，判 down 时样本年龄 %v —— 应当盖住它、且宽不出一个周期（#48）",
			got, downAt)
	}
}

// TestHeartbeatGapIgnoresOutliersAndReconnects：一次停顿、一次断线重连不会把估计带走。
//
// 估计被一次断线撑大的话，下一次真宕机要等那么久才判 down——
// 而那正是这个估计最该准的时候。
func TestHeartbeatGapIgnoresOutliersAndReconnects(t *testing.T) {
	const id = "node-hk-01"
	m := New(Config{Interval: time.Second, Threshold: 3})
	seed(m, id)
	at := time.Now().Add(-time.Hour)
	step := func(d time.Duration) { at = at.Add(d); m.observeAt(hbFor(id), at) }

	m.observeAt(hbFor(id), at)
	step(3 * time.Second)
	step(3 * time.Second)
	step(3 * time.Second)
	step(20 * time.Second) // 同一条隧道上停顿了一下——而且是最近的那一个
	if got := m.gapOf(id); got != 3*time.Second {
		t.Fatalf("一次停顿之后估计 = %v，想要 3s", got)
	}

	// 断线重连：断开那一大段不是心跳间隔。连着断三次——一次的话中位数
	// 自己就扛住了，这条测试也就证明不了 Alive 那一步在不在。
	for range 3 {
		m.Alive(id)
		step(5 * time.Minute)
	}
	if got := m.gapOf(id); got != 3*time.Second {
		t.Fatalf("反复重连之后估计 = %v，断开那几段被算成了心跳间隔", got)
	}
}

// TestUnmeasuredNodeAssumesAgentDefault：还没量出间隔的节点，按 Agent 的默认心跳间隔判。
//
// 主控刚启动、warm 装进来的节点一个间隔都没有。按判定周期判的话，
// 周期设成 1 秒时它们在重连回来之前就被全体判 down。
func TestUnmeasuredNodeAssumesAgentDefault(t *testing.T) {
	m := New(Config{Interval: time.Second, Threshold: 3})
	if got := m.gapOf("node-never-seen"); got != DefaultAssumedHeartbeat {
		t.Fatalf("没量过的节点估计 = %v，想要 %v", got, DefaultAssumedHeartbeat)
	}
}

// seed 先放一个 status=ok 的条目：否则第一次心跳会因为状态从空变成 ok
// 去写一条事件，而这几条测试不带库。
func seed(m *Monitor, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes[id] = &nodeState{status: "ok"}
}

func hbFor(id string) tunnel.Heartbeat { return tunnel.Heartbeat{NodeID: id} }

func (m *Monitor) isDown(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nodes[id].downSent
}
