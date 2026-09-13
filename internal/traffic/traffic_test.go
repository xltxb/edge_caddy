package traffic_test

import (
	"context"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/health"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
	"github.com/xltxb/edge_caddy/internal/traffic"
)

type fakeHealth map[string]health.Sample

func (f fakeHealth) Latest(id string) (health.Sample, bool) {
	s, ok := f[id]
	return s, ok
}

// fresh 把一份样本标成「刚报上来的」。
//
// Totals 现在看新鲜度（issue #48），而 At 的零值是很久以前——不标的话
// 这些测试造的全是「冻结样本」，而它们想说的恰恰相反。
func fresh(s health.Sample) health.Sample {
	s.At = time.Now()
	return s
}

func seedNode(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.UpsertNode(context.Background(), store.NodeSpec{
		NodeID: id, City: "香港", Vendor: "DMIT", Line: "CN2 GIA", PublicIP: "203.0.113.7",
	}); err != nil {
		t.Fatal(err)
	}
}

// **「conns=0」在「都报了 0」和「一台都没报」之间完全不同，而两者的和都是 0。**
//
// Totals 因此回报 reported —— 采样器靠它判断这一分钟的数字可不可信。
func TestTotalsReportsHowManyNodesAnswered(t *testing.T) {
	nodes := []store.Node{{ID: "a"}, {ID: "b"}}
	h := fakeHealth{"a": fresh(health.Sample{Conns: 3, ReqTotal: 100, OriginTotal: 40})}

	conns, req, origin, reported := traffic.Totals(nodes, h)
	if conns != 3 || req != 100 || origin != 40 {
		t.Fatalf("汇总错了：conns=%d req=%d origin=%d", conns, req, origin)
	}
	if reported != 1 {
		t.Fatalf("只有 a 报了数，reported 应当是 1，实际 %d", reported)
	}

	// 一台都没报时，和是 0 而 reported 也是 0 —— 调用方据此区分
	// 「没有流量」与「没有数据」。
	_, _, _, none := traffic.Totals(nodes, fakeHealth{})
	if none != 0 {
		t.Fatalf("没人报数时 reported 应当是 0，实际 %d", none)
	}
}

// 已下线的节点既不参与流量，也不参与「应该有几台报数」。
//
// 不排除的话，下线一台机器之后 reported 会永远小于节点总数，
// **采样从此永久跳过** —— 而那正好是最难发现的一种坏法：
// 没有报错，只是从某一天起再也没有新样本。
func TestDrainedNodeIsExcludedFromTotals(t *testing.T) {
	at := time.Now()
	nodes := []store.Node{{ID: "a"}, {ID: "b", DrainedAt: &at}}
	h := fakeHealth{
		"a": fresh(health.Sample{Conns: 3}),
		"b": fresh(health.Sample{Conns: 99}), // 它还在报，但它已经被人下线了
	}
	conns, _, _, reported := traffic.Totals(nodes, h)
	if conns != 3 {
		t.Errorf("已下线节点的连接数不该算进来：conns=%d", conns)
	}
	if reported != 1 {
		t.Errorf("已下线节点不该算进 reported：%d", reported)
	}
}

// **报数的节点不齐就不记这一分钟。**
//
// 三种情况都会让数字偏低：节点刚接入、心跳丢了、主控刚起来。
// 而偏低的样本在 24 小时后会成为分母，产生一个假的巨大涨幅
// —— 那时人早忘了昨天发生过什么。
func TestSampleIsSkippedWhenNodesAreMissing(t *testing.T) {
	st := testdb.New(t)
	seedNode(t, st, "a")
	seedNode(t, st, "b")

	s := &traffic.Sampler{Store: st, Health: fakeHealth{"a": fresh(health.Sample{Conns: 5})},
		Interval: 10 * time.Millisecond, Warmup: time.Nanosecond}
	runFor(t, s, 120*time.Millisecond)

	if n := countSamples(t, st); n != 0 {
		t.Fatalf("只有一台报数，不该记下样本，实际有 %d 行", n)
	}

	// 两台都报了就记。
	s2 := &traffic.Sampler{Store: st,
		Health:   fakeHealth{"a": fresh(health.Sample{Conns: 5}), "b": fresh(health.Sample{Conns: 7})},
		Interval: 10 * time.Millisecond, Warmup: time.Nanosecond}
	runFor(t, s2, 120*time.Millisecond)

	if n := countSamples(t, st); n == 0 {
		t.Fatal("两台都报了数，应当记下样本")
	}
}

// 一台节点都没有时不记 0 —— 那不是「流量为零」，是「还没有系统」。
//
// 记了的话，第一台节点接入之后的第二天会出现一个从 0 起算的涨幅。
func TestNoSampleWhenThereAreNoNodes(t *testing.T) {
	st := testdb.New(t)
	s := &traffic.Sampler{Store: st, Health: fakeHealth{},
		Interval: 10 * time.Millisecond, Warmup: time.Nanosecond}
	runFor(t, s, 80*time.Millisecond)
	if n := countSamples(t, st); n != 0 {
		t.Fatalf("没有节点时不该记样本，实际 %d 行", n)
	}
}

// **主控刚启动的那几分钟不采样。**
//
// 那时 health 的内存是空的，节点要重连（Agent 退避封顶 30 秒）再发一次心跳
// 才会重新有数。这期间采到的点会在 24 小时后成为分母。
func TestWarmupSkipsEarlySamples(t *testing.T) {
	st := testdb.New(t)
	seedNode(t, st, "a")

	s := &traffic.Sampler{Store: st, Health: fakeHealth{"a": fresh(health.Sample{Conns: 5})},
		Interval: 10 * time.Millisecond, Warmup: time.Hour}
	runFor(t, s, 100*time.Millisecond)

	if n := countSamples(t, st); n != 0 {
		t.Fatalf("预热期内不该记样本，实际 %d 行", n)
	}
}

// 昨天那一分钟没采到就返回 nil，**不往前找最近的**。
//
// 往前找会悄悄改变「同时段」的含义，而那个含义正是这个数字的全部意义。
func TestDeltaIsNilWhenYesterdayHasNoSample(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)

	// 前天有样本，昨天这一分钟没有。
	if err := st.InsertTrafficSample(ctx, store.TrafficSample{
		At: now.Add(-48 * time.Hour), ConnsTotal: 100,
	}); err != nil {
		t.Fatal(err)
	}
	d, reason, err := traffic.DeltaPct(ctx, st, now, 200)
	if err != nil {
		t.Fatal(err)
	}
	if d != nil {
		t.Fatalf("昨天那一分钟没样本，应当是 nil，实际 %v —— 前天的数不是「同时段」", *d)
	}
	// **原因要说得准。** 前天有样本，说明历史够长了 —— 这一次是那一分钟
	// 正好没采到（主控停着、或者报数节点不齐被跳过），不是「再等等就有」。
	// 说成「历史不足」会让人白等一天。
	if reason != traffic.ReasonNoSampleAtThatTime {
		t.Errorf("原因 = %q，想要 %q", reason, traffic.ReasonNoSampleAtThatTime)
	}
}

// 库里一条样本都没有、或者最早的还不到 24 小时前 —— 那才是「再等等就有」。
func TestReasonIsInsufficientHistoryWhenTooYoung(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)

	// 一条都没有。
	if _, reason, err := traffic.DeltaPct(ctx, st, now, 100); err != nil {
		t.Fatal(err)
	} else if reason != traffic.ReasonInsufficientHistory {
		t.Errorf("空库时原因 = %q，想要 %q", reason, traffic.ReasonInsufficientHistory)
	}

	// 只有一小时前的样本，历史仍然不够 24 小时。
	if err := st.InsertTrafficSample(ctx, store.TrafficSample{
		At: now.Add(-time.Hour), ConnsTotal: 50,
	}); err != nil {
		t.Fatal(err)
	}
	if _, reason, err := traffic.DeltaPct(ctx, st, now, 100); err != nil {
		t.Fatal(err)
	} else if reason != traffic.ReasonInsufficientHistory {
		t.Errorf("历史不足时原因 = %q，想要 %q", reason, traffic.ReasonInsufficientHistory)
	}
}

func TestDeltaPct(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)

	if err := st.InsertTrafficSample(ctx, store.TrafficSample{
		At: now.Add(-24 * time.Hour), ConnsTotal: 200,
	}); err != nil {
		t.Fatal(err)
	}
	d, reason, err := traffic.DeltaPct(ctx, st, now, 250)
	if err != nil {
		t.Fatal(err)
	}
	if d == nil || *d < 24.9 || *d > 25.1 {
		t.Fatalf("200 → 250 应当是 +25%%，实际 %v", d)
	}
	// **有数字时原因必须为空。** 一个同时给出数字和「为什么没有数字」的
	// 响应，会让人怀疑那个数字。
	if reason != "" {
		t.Errorf("有数字时不该带原因，实际 %q", reason)
	}

	// 可以为负。
	d2, _, _ := traffic.DeltaPct(ctx, st, now, 150)
	if d2 == nil || *d2 > -24.9 {
		t.Fatalf("200 → 150 应当是 -25%%，实际 %v", d2)
	}
}

// **分母为 0 时百分比没有定义，返回 nil。**
//
// 昨天 0 连接、今天 100，那确实是「从无到有」，但它不是一个百分比。
// 硬算会得到 +Inf 或者一个靠加 1 平滑出来的假数字，而两者都比
// 「暂无同比」更难被质疑。
func TestDeltaIsNilWhenYesterdayWasZero(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)

	if err := st.InsertTrafficSample(ctx, store.TrafficSample{
		At: now.Add(-24 * time.Hour), ConnsTotal: 0,
	}); err != nil {
		t.Fatal(err)
	}
	d, reason, err := traffic.DeltaPct(ctx, st, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if d != nil {
		t.Fatalf("昨天是 0，百分比没有定义，应当返回 nil，实际 %v", *d)
	}
	if reason != traffic.ReasonZeroBaseline {
		t.Errorf("原因 = %q，想要 %q —— 这一种跟「没有样本」不同："+
			"样本是有的，只是它当不了分母", reason, traffic.ReasonZeroBaseline)
	}
}

// 保留 7 天：不清的话表会一直长。
// 「不算大」不是「不用管」——一个只写不清的表是一条没人读的注释的数据版本。
func TestPruneDropsOldSamplesOnly(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)

	for _, ago := range []time.Duration{8 * 24 * time.Hour, 6 * 24 * time.Hour, time.Hour} {
		if err := st.InsertTrafficSample(ctx, store.TrafficSample{
			At: now.Add(-ago), ConnsTotal: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.PruneTrafficSamples(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应当只删掉那条 8 天前的，实际删了 %d", n)
	}
	if left := countSamples(t, st); left != 2 {
		t.Fatalf("应当剩 2 行，实际 %d", left)
	}
}

// 同一分钟重复采样是幂等的，**先写的赢**。
//
// 主控重启前那一分钟的样本是节点齐全时采的；重启后同一分钟再采一次
// 可能只有半数节点回来了，那个数字更假。
func TestSameMinuteKeepsTheFirstSample(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	at := time.Now().Truncate(time.Minute)

	if err := st.InsertTrafficSample(ctx, store.TrafficSample{At: at, ConnsTotal: 500}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertTrafficSample(ctx, store.TrafficSample{At: at, ConnsTotal: 7}); err != nil {
		t.Fatal(err)
	}
	smp, ok, err := st.TrafficAt(ctx, at)
	if err != nil || !ok {
		t.Fatalf("样本应当存在：ok=%v err=%v", ok, err)
	}
	if smp.ConnsTotal != 500 {
		t.Fatalf("应当保留先写的 500，实际 %d", smp.ConnsTotal)
	}
}

func runFor(t *testing.T, s *traffic.Sampler, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	s.Run(ctx)
}

func countSamples(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM traffic_samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// **reason 的取值集合是契约的一部分，加一种就得改契约。**
//
// 前端 agent 为「后端加了第四种而我没跟上」加了一支防御：认不出的取值
// 退回中性文案。那是对的方向——最坏结果该是「少说一句」，
// 不该是「把一个不认识的原因说成某个认识的」。
//
// 但那只兜住了他那一侧。这一条盯的是我这一侧：**新增一种取值时这里会红**，
// 而红的时候人该做的第一件事是去改契约 §3 那张表，不是把常量加进这个列表。
func TestReasonValuesAreExactlyWhatTheContractLists(t *testing.T) {
	// 契约 docs/api-contract.md §3 的表里就这三行。
	inContract := map[string]bool{
		"insufficient_history": true,
		"no_sample":            true,
		"zero_baseline":        true,
	}
	for _, r := range []string{
		traffic.ReasonInsufficientHistory,
		traffic.ReasonNoSampleAtThatTime,
		traffic.ReasonZeroBaseline,
	} {
		if !inContract[r] {
			t.Errorf("常量 %q 不在契约 §3 那张表里", r)
		}
		delete(inContract, r)
	}
	if len(inContract) > 0 {
		t.Errorf("契约里列了但代码里没有：%v", inContract)
	}

	// 而且真跑出来的原因也只能是这三个之一 —— 上面那段只比对常量，
	// 一个直接 return 字面量的分支绕得过它。
	st := testdb.New(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)
	known := map[string]bool{
		traffic.ReasonInsufficientHistory: true,
		traffic.ReasonNoSampleAtThatTime:  true,
		traffic.ReasonZeroBaseline:        true,
		"":                                true, // 有数字时
	}

	// 走遍三条路：空库、有旧样本但那一分钟没有、那一分钟是 0。
	check := func(label string) {
		t.Helper()
		_, reason, err := traffic.DeltaPct(ctx, st, now, 100)
		if err != nil {
			t.Fatal(err)
		}
		if !known[reason] {
			t.Errorf("%s：跑出了契约没列的原因 %q", label, reason)
		}
	}
	check("空库")
	if err := st.InsertTrafficSample(ctx, store.TrafficSample{
		At: now.Add(-48 * time.Hour), ConnsTotal: 10,
	}); err != nil {
		t.Fatal(err)
	}
	check("有旧样本但那一分钟没有")
	if err := st.InsertTrafficSample(ctx, store.TrafficSample{
		At: now.Add(-24 * time.Hour), ConnsTotal: 0,
	}); err != nil {
		t.Fatal(err)
	}
	check("那一分钟是 0")
}

// TestStaleSamplesDoNotCountAsReported：一份冻结的样本不算「报了数」。
//
// Totals 只看条目在不在，不看它有多旧（issue #48）。一台掉线但还没被删除的
// 节点，health 里那条 Sample 会一直留着——于是：
//
//   - reported 照数它，`报数不齐就不记` 那道闸被绕过去了
//   - 它一小时前的连接数被加进当下的汇总
//
// 而 sampleOnce 的注释把这道闸的理由说得很清楚：「放宽它会让偏低的样本进库，
// 而 24 小时后那个样本会成为同比的分母」。这里不是放宽，是它**看的东西不对**
// ——闸拦的是「节点数不够」，而不是「数据是不是当下的」。
//
// Forget 的唯一调用点是删节点（api/nodemeta.go），掉线不会清。
func TestStaleSamplesDoNotCountAsReported(t *testing.T) {
	now := time.Now()
	h := fakeHealth{
		"node-a": {Conns: 100, ReqTotal: 1000, At: now},
		// b 一小时前掉线了，样本冻在那儿——**节点还没被删，所以条目还在**。
		"node-b": {Conns: 900, ReqTotal: 9000, At: now.Add(-time.Hour)},
	}
	nodes := []store.Node{{ID: "node-a"}, {ID: "node-b"}}

	conns, req, _, reported := traffic.Totals(nodes, h)

	if reported != 1 {
		t.Errorf("只有一台真的在报数，reported 是 %d —— "+
			"「报数不齐就不记」那道闸会被一份冻结的样本绕过去", reported)
	}
	if conns != 100 {
		t.Errorf("连接数是 %d，想要 100 —— "+
			"一台机器一小时前的数字被加进了当下的汇总", conns)
	}
	if req != 1000 {
		t.Errorf("请求数是 %d，想要 1000", req)
	}
}

// TestSamplingSurvivesANodeBeingDown：一台机器宕着，采样不能整个停摆。
//
// `want` 是「未下线的全部节点」（`CountUndrainedNodes`：`drained_at IS NULL`），
// 而 #48 之后一台**宕机但没被删、也没走下线流程**的机器永远进不了 `reported`
// ——于是 `reported < want` 恒成立，**整个集群一个样本都不再入库**，
// 直到人把那台机器删掉或下线。
//
// 那些样本正是「较昨日同时段」的分母：一次过夜故障之后，第二天整天没有同比。
// 而日志只有 Debug 级，没有任何地方会说出来。
//
// 「报数不齐就不记」这条闸本身是对的，它拦的是「数字偏低」；一台 health 已经
// 判成 down 的机器不该被算进「应该报数的节点」——它**本来就不该报**。
func TestSamplingSurvivesANodeBeingDown(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()

	for _, id := range []string{"node-a", "node-b"} {
		if err := st.UpsertNode(ctx, store.NodeSpec{
			NodeID: id, City: "香港", Vendor: "v", Line: "l", PublicIP: "203.0.113.7",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// b 宕了：health 把它标成 down，但没人删它、也没人点下线。
	if err := st.SetNodeDown(ctx, "node-b"); err != nil {
		t.Fatal(err)
	}

	s := &traffic.Sampler{
		Store: st,
		// 只有 a 在报数，而它是唯一该报数的那台。
		Health:   fakeHealth{"node-a": fresh(health.Sample{Conns: 5, ReqTotal: 100})},
		Interval: 10 * time.Millisecond, Warmup: time.Nanosecond,
	}
	runFor(t, s, 120*time.Millisecond)

	if n := countSamples(t, st); n == 0 {
		t.Error("一台机器宕着，整个集群就再也不记样本了 —— " +
			"而这些样本是「较昨日同时段」的分母，一次过夜故障会让第二天整天没有同比")
	}
}

// TestStaleWindowFollowsTheConfiguredHeartbeat：「当下」的界线要跟着心跳配置走。
//
// staleAfter 原先写死 30 秒，注释的理由是「心跳默认 3 秒一次，离线判定要连续
// 错过 9 秒，30 秒给足了抖动余量」。**那句话只在默认值上成立。**
//
// `PUT /settings` 允许 heartbeat_interval_s 到 60、offline_threshold_count
// 到 20（internal/api/settings.go 的校验），也就是判离线最长要 1200 秒。
// 把心跳间隔调到 30 秒——一个完全合法的设置——每一份样本在下一次心跳到达前
// 都已经「超过 30 秒」，于是 reported 恒为 0，而 want 数的是活着的节点：
//
//	reported < want  →  每一分钟都跳过  →  **采样永久停摆**
//
// 而它是静默的：没有报错，只有总览上那个「较昨日同时段」一直空着，
// 24 小时后也不会好——因为库里根本没有昨天那个点。
//
// 判据是「一份刚好在一个心跳周期内到达的样本算不算数」：那是这条界线
// 唯一要回答的问题。
func TestStaleWindowFollowsTheConfiguredHeartbeat(t *testing.T) {
	// 运维把心跳调慢到 30 秒、阈值 3 次（判离线 90 秒）——都在允许范围内。
	const interval, threshold = 30 * time.Second, 3

	nodes := []store.Node{{ID: "node-hk-01", Status: "up"}}
	h := fakeHealth{"node-hk-01": health.Sample{
		// 上一次心跳是 40 秒前：比写死的 30 秒旧，但远没到判离线的 90 秒。
		// 这台机器**活得好好的**，它只是心跳慢。
		At: time.Now().Add(-40 * time.Second), Conns: 12, ReqTotal: 300, OriginTotal: 40,
	}}

	_, _, _, reported := traffic.TotalsWithin(nodes, h, traffic.StaleWindow(interval, threshold))
	if reported != 1 {
		t.Fatalf("reported = %d，想要 1 —— 心跳调到 %v 之后每一份样本都比写死的"+
			"30 秒旧，reported 恒为 0，而 want 数的是活着的节点：采样每一分钟"+
			"都跳过，且没有任何报错", reported, interval)
	}
}

// 另一半：真的过期了还是要判出来，否则这条界线等于没有。
//
// 没有这一条，一个「永远不判陈旧」的实现也能让上面那条过——而那正是
// issue #48 修掉的东西（掉线节点的旧数字被加进当下的汇总）。
func TestStaleWindowStillCatchesARealStaleSample(t *testing.T) {
	const interval, threshold = 3 * time.Second, 3

	nodes := []store.Node{{ID: "node-hk-01", Status: "up"}}
	h := fakeHealth{"node-hk-01": health.Sample{
		At: time.Now().Add(-10 * time.Minute), Conns: 12,
	}}

	_, _, _, reported := traffic.TotalsWithin(nodes, h, traffic.StaleWindow(interval, threshold))
	if reported != 0 {
		t.Fatalf("reported = %d，想要 0 —— 十分钟前的样本不是「当下」，"+
			"把它算进汇总就是 issue #48：一个偏低的数字在 24 小时后成为同比的分母",
			reported)
	}
}
