// Package traffic 采集全局流量样本，供总览的「较昨日同时段」同比。
package traffic

import (
	"context"
	"log/slog"
	"time"

	"github.com/xltxb/edge_caddy/internal/health"
	"github.com/xltxb/edge_caddy/internal/store"
)

// Latester 是采样需要的那一点点观测面。
type Latester interface {
	Latest(nodeID string) (health.Sample, bool)
}

// Totals 汇总当前全局流量，并回报**有几台节点报了数**。
//
// 抽出来是因为 overview 和采样器要用同一份口径：两处各算一遍，
// 迟早在界面上给出两个对不上的数字，而那比单个错数字更让人怀疑整个系统。
//
// reported 是判断这份汇总可不可信的依据——一个「conns=0」在
// 「三台都报了 0」和「一台都没报」之间完全不同，而两者的和都是 0。
func Totals(nodes []store.Node, h Latester) (conns, req, origin uint64, reported int) {
	if h == nil {
		return 0, 0, 0, 0
	}
	for _, n := range nodes {
		if n.DrainedAt != nil {
			// 已下线的机器不参与：它不该被算进流量，也不该被算进
			// 「应该有多少台在报数」。
			continue
		}
		m, ok := h.Latest(n.ID)
		if !ok {
			continue
		}
		// **样本要是当下的。**
		//
		// 这里原先只看条目在不在。一台掉线但还没被删除的节点，health 里那条
		// Sample 会一直留着（Forget 的唯一调用点是删节点），于是 reported 照数
		// 它、它一小时前的连接数被加进当下的汇总（issue #48）。
		//
		// 后果不在这一刻：sampleOnce 的「报数不齐就不记」被绕过去之后，
		// 那个偏低的样本会入库，而 24 小时后它成为同比的分母。
		if time.Since(m.At) > staleAfter {
			continue
		}
		reported++
		conns += uint64(m.Conns)
		req += m.ReqTotal
		origin += m.OriginTotal
	}
	return conns, req, origin, reported
}

// staleAfter 是一份样本还算不算「当下」的界线。
//
// 取 30 秒：心跳默认 3 秒一次，离线判定要连续错过 9 秒。30 秒给足了抖动的
// 余量，又远短于一分钟一次的采样周期——一台真掉线的机器不会带着旧数字
// 混进下一个采样点。
const staleAfter = 30 * time.Second

// warmup 是主控启动后不采样的那段。
//
// **主控重启之后 health 的内存是空的**，节点要重连（Agent 的退避封顶 30 秒）
// 再发一次心跳才会重新有数。这期间采到的是一个远低于真实值的点，
// 而 24 小时后它会成为同比的分母，产生一个假的巨大涨幅——
// 那时人早忘了昨天重启过。
//
// **一个 +340% 比一个 null 危险得多：null 会让人去查，一个具体的百分比不会。**
//
// 取 3 分钟：退避封顶 30 秒 + 心跳周期，留足余量。宁可缺几个样本，
// 也不要假样本——缺的那一分钟只影响一个点，假的那一分钟会在 24 小时后再骗一次。
const warmup = 3 * time.Minute

// Sampler 每分钟把全局流量记一行。
type Sampler struct {
	Store  *store.Store
	Health Latester
	Log    *slog.Logger

	// Interval / Retention 为 0 时用默认值（1 分钟 / 7 天）。
	Interval  time.Duration
	Retention time.Duration
	// Warmup 为 0 时用 warmup。测试会把它调小。
	Warmup time.Duration
}

func (s *Sampler) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Run 跑到 ctx 结束。
func (s *Sampler) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	warm := s.Warmup
	if warm <= 0 {
		warm = warmup
	}
	started := time.Now()

	tick := time.NewTicker(interval)
	defer tick.Stop()
	// 清理跟采样同一个循环，每天一次即可——单开一个 goroutine 只是多一处
	// 要记得停的东西。
	lastPrune := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if now.Sub(started) < warm {
				continue
			}
			if err := s.sampleOnce(ctx, now); err != nil {
				s.log().Error("采集流量样本失败", "err", err)
			}
			if now.Sub(lastPrune) >= 24*time.Hour {
				lastPrune = now
				s.prune(ctx, now)
			}
		}
	}
}

// sampleOnce 采一分钟。**报数的节点不齐就不记。**
func (s *Sampler) sampleOnce(ctx context.Context, now time.Time) error {
	nodes, err := s.Store.ListNodes(ctx)
	if err != nil {
		return err
	}
	conns, req, origin, reported := Totals(nodes, s.Health)

	want, err := s.Store.CountExpectedReporters(ctx)
	if err != nil {
		return err
	}
	if want == 0 {
		// 一台节点都没有。这不是「流量为零」，是「还没有系统」——
		// 记一行 0 会让第一台节点接入后的第二天出现一个从 0 起算的涨幅。
		return nil
	}
	// **报数不齐就不记**，由 scripts/probes.py 的「流量采样-报数不齐就不记」盯着。
	// 放宽它会让偏低的样本进库，而 24 小时后那个样本会成为同比的分母。
	if reported < want {
		// 有节点没报数：可能刚接入、可能心跳丢了、也可能主控刚起来。
		// 三种都让这一分钟的数字偏低，而偏低的样本在 24 小时后会骗人。
		s.log().Debug("流量样本跳过：报数节点不齐",
			"reported", reported, "want", want)
		return nil
	}
	return s.Store.InsertTrafficSample(ctx, store.TrafficSample{
		At: now, ConnsTotal: conns, ReqTotal: req, OriginTotal: origin,
	})
}

func (s *Sampler) prune(ctx context.Context, now time.Time) {
	retention := s.Retention
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	n, err := s.Store.PruneTrafficSamples(ctx, now.Add(-retention))
	if err != nil {
		s.log().Error("清理流量样本失败", "err", err)
		return
	}
	if n > 0 {
		s.log().Info("清理流量样本", "deleted", n, "retention", retention)
	}
}

// 没有同比数字时的原因。**空字符串表示「有数字」。**
//
// 前端 agent 指出的：一个不带原因的 null 逼着界面在三种情况里挑一句话说，
// **而挑错的那两次会让人白等**——「历史不足」说的是「再等等」，
// 而真相可能是「主控昨天那会儿停着」，等到明天也不会变。
//
// 后端当场就知道是哪一种，那就说出来。
const (
	ReasonInsufficientHistory = "insufficient_history" // 库里最早的样本还不到 24 小时前
	ReasonNoSampleAtThatTime  = "no_sample"            // 有更早的样本，但昨天那一分钟没有
	ReasonZeroBaseline        = "zero_baseline"        // 昨天那一分钟是 0，百分比没有定义
)

// DeltaPct 算「较昨日同时段」的连接数变化百分比。
//
// 返回 nil 表示**没有可比的数**，那与「持平」是两回事——0 会被读成持平，
// 而前端要按空态处理（api-contract §3）。第二个返回值说明是哪一种没有。
func DeltaPct(ctx context.Context, st *store.Store, now time.Time, current uint64) (*float64, string, error) {
	prev, ok, err := st.TrafficAt(ctx, now.Add(-24*time.Hour))
	if err != nil {
		return nil, "", err
	}
	if !ok {
		// 分辨「历史还不够长」与「那一分钟正好没采到」：两者都表现为查不到，
		// 而对人的意思完全不同——前者再等等就有了，后者说明主控那时停着
		// 或者那一分钟因为报数节点不齐被跳过了。
		earliest, has, err := st.EarliestTrafficSample(ctx)
		if err != nil {
			return nil, "", err
		}
		if !has || earliest.After(now.Add(-24*time.Hour)) {
			return nil, ReasonInsufficientHistory, nil
		}
		return nil, ReasonNoSampleAtThatTime, nil
	}
	if prev.ConnsTotal == 0 {
		// **分母为 0 时百分比没有定义。**
		//
		// 昨天 0 连接、今天 100，那确实是「从无到有」，但它不是一个百分比。
		// 硬算会得到 +Inf 或者一个靠加 1 平滑出来的假数字，
		// 而两者都比「暂无同比」更难被质疑。
		return nil, ReasonZeroBaseline, nil
	}
	d := (float64(current) - float64(prev.ConnsTotal)) / float64(prev.ConnsTotal) * 100
	return &d, "", nil
}
