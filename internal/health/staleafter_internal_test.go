package health

import (
	"context"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
)

// TestStaleAfterIsTheTightBoundOnGoingDown：StaleAfter 正好盖住判 down 的最坏窗口。
//
// **这是 health 对外说的那句话**：一份样本旧到这个程度，这台机器在我这儿
// 已经是 down 了。采样拿它去判「这份样本还算不算当下」，两套判据因此同源
// （见 traffic.Latester）。说小了，样本被判陈旧而节点还在 want 里，
// reported < want，整分钟的采样被静默跳过；说大了，已判离线的机器的旧数字
// 被加进当下的汇总（issue #48）。
//
// # 为什么手工驱动 tick 而不是等墙钟
//
// 墙钟量出来的是「告警落地时刻」，它比判 down 晚一段写库的 I/O，而
// StaleAfter 与判 down 的最坏时刻**本就设计成相等**——于是任何调度抖动都会
// 让断言红，红得还像是代码的问题。手工驱动把相位变成一个可以指定的量。
//
// # 最坏相位
//
// tick 的相位与心跳到达的时刻无关。心跳刚落地就 tick 的话，那一次看到的
// 间隔不够一个周期，misses 不增——最坏要多花一个周期，于是判 down 时样本的
// 年龄趋近 Interval×(Threshold+1) 而不是 ×Threshold。
func TestStaleAfterIsTheTightBoundOnGoingDown(t *testing.T) {
	const interval, threshold = time.Second, 3
	const id = "node-hk-01"

	// 真库：走到判 down 那一步会 markDown（摘解析、写状态、发告警）。
	st := testdb.New(t)
	if err := st.UpsertNode(context.Background(), store.NodeSpec{
		NodeID: id, City: "香港", Vendor: "v", Line: "l", PublicIP: "203.0.113.7",
	}); err != nil {
		t.Fatal(err)
	}
	m := New(Config{Store: st, Interval: interval, Threshold: threshold})

	m.mu.Lock()
	m.nodes[id] = &nodeState{seen: true, last: Sample{At: time.Now()}}
	m.mu.Unlock()

	// age 把这份样本的年龄调成指定值，下一次 tick 就按这个年龄判。
	age := func(d time.Duration) {
		m.mu.Lock()
		m.nodes[id].last.At = time.Now().Add(-d)
		m.mu.Unlock()
	}
	isDown := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.nodes[id].downSent
	}

	// 最坏相位：第一次 tick 落在心跳刚到之后，差一点点不到一个周期。
	const phase = interval - time.Millisecond
	ctx := context.Background()

	age(phase)
	m.tick(ctx)
	if isDown() {
		t.Fatal("装置坏了：不到一个周期的间隔不该记一次 miss")
	}

	// 此后每个周期都错过一次。第 threshold 次之后才该 down。
	for k := 1; k <= threshold; k++ {
		age(phase + time.Duration(k)*interval)
		m.tick(ctx)

		if k < threshold && isDown() {
			t.Fatalf("错过 %d 次就判 down 了，而阈值是 %d —— "+
				"判早了会把一次寻常的抖动当成离线", k, threshold)
		}
	}
	if !isDown() {
		t.Fatalf("错过 %d 次仍没判 down —— 装置或阈值判据坏了", threshold)
	}

	// **判 down 那一刻，样本的年龄就是这个。** StaleAfter 必须盖得住它。
	worst := phase + threshold*interval
	if m.StaleAfter() < worst {
		t.Fatalf("StaleAfter = %v，而最坏情况下判 down 时样本已经 %v 了 —— "+
			"这段差值里样本算陈旧、节点算活着，reported < want，"+
			"采样被静默跳过", m.StaleAfter(), worst)
	}
	// 另一半：界也不能松。松了就是 issue #48——一台已经被判离线的机器，
	// 它的旧数字还被算进当下的汇总，24 小时后成为同比的分母。
	if m.StaleAfter() > worst+interval {
		t.Fatalf("StaleAfter = %v，比最坏的 %v 还宽出一个周期以上 —— "+
			"已判离线的机器的旧数字会被算进当下的汇总（issue #48）",
			m.StaleAfter(), worst)
	}
}
