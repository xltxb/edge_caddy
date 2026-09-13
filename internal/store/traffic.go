package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// TrafficSample 是某一分钟的全局流量快照。
//
// 全系统唯一落库的时序数据，只为总览的「较昨日同时段」同比。
// 节点级的 CPU sparkline 仍然只在主控进程内存里——那个不需要跨天。
type TrafficSample struct {
	At          time.Time `json:"at"`
	ConnsTotal  uint64    `json:"conns_total"`
	ReqTotal    uint64    `json:"req_total"`
	OriginTotal uint64    `json:"origin_total"`
}

// InsertTrafficSample 写一分钟的样本。
//
// at 由调用方对齐到整分钟：主键是 at，对齐之后同一分钟重复采样是幂等的
// （ON CONFLICT DO NOTHING）——**先写的那个赢**。这跟「后写的赢」的区别在
// 主控重启后有意义：重启前那一分钟的样本是节点齐全时采的，重启后同一分钟
// 再采一次可能只有半数节点回来了，那个数字更假。
func (s *Store) InsertTrafficSample(ctx context.Context, smp TrafficSample) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO traffic_samples (at, conns_total, req_total, origin_total)
		 VALUES ($1,$2,$3,$4) ON CONFLICT (at) DO NOTHING`,
		smp.At.UTC().Truncate(time.Minute),
		int64(smp.ConnsTotal), int64(smp.ReqTotal), int64(smp.OriginTotal))
	return err
}

// TrafficAt 读**精确那一分钟**的样本，没有就返回 false。
//
// **不往前找最近的。** 往前找会悄悄改变「同时段」的含义——而那个含义正是
// 这个数字的全部意义。昨天这一分钟没采到（主控停机、或者跳过了窗口期），
// 诚实的回答是「没有可比的数」，不是「拿前天某个时刻凑一个」。
func (s *Store) TrafficAt(ctx context.Context, at time.Time) (TrafficSample, bool, error) {
	var smp TrafficSample
	var conns, req, origin int64
	err := s.Pool.QueryRow(ctx,
		`SELECT at, conns_total, req_total, origin_total
		 FROM traffic_samples WHERE at = $1`,
		at.UTC().Truncate(time.Minute)).Scan(&smp.At, &conns, &req, &origin)
	if errors.Is(err, pgx.ErrNoRows) {
		return smp, false, nil
	}
	if err != nil {
		return smp, false, err
	}
	smp.ConnsTotal, smp.ReqTotal, smp.OriginTotal = uint64(conns), uint64(req), uint64(origin)
	return smp, true, nil
}

// PruneTrafficSamples 删掉 before 之前的样本，返回删了几行。
//
// 没有这一步的话表会一直长。7 天 ≈ 10080 行不算大，但「不算大」不是
// 「不用管」——一个只写不清的表是一条没人读的注释的数据版本。
func (s *Store) PruneTrafficSamples(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx,
		`DELETE FROM traffic_samples WHERE at < $1`, before.UTC())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CountUndrainedNodes 数还没被人下线的节点。
//
// 采样器用它判断「这一分钟的数字可信吗」：已下线的机器不该被算进
// 「应该有多少台在报数」——否则下线一台机器之后采样会永久跳过。
func (s *Store) CountUndrainedNodes(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM edge_nodes WHERE drained_at IS NULL`).Scan(&n)
	return n, err
}

// CountExpectedReporters 数「此刻**应该**报数的节点」。
//
// 与 CountUndrainedNodes 的区别是它还排除 status='down' 的机器。采样那条
// 「报数不齐就不记」的闸拦的是**数字偏低**——而一台 health 已经判成 down 的
// 机器本来就不该报数，把它算进分母会让闸恒关：一台宕机但没被删、也没走下线
// 流程的机器，会让整个集群再也不记样本，直到有人去动它（issue #48 的修复引入）。
//
// 下线（drained）与宕机（down）都排除，但理由不同：前者是**意图**（人说它
// 不该承载流量），后者是**事实**（它此刻报不出数）。CONTEXT.md 把这两件事
// 分开记，这里也分开写。
func (s *Store) CountExpectedReporters(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM edge_nodes WHERE drained_at IS NULL AND status <> 'down'`).Scan(&n)
	return n, err
}

// EarliestTrafficSample 返回最早那条样本的时刻，一条都没有时返回 false。
//
// 用来分辨「历史还不够长」与「昨天那一分钟正好没采到」——两者都表现为
// 「查不到昨天那一分钟」，而对人的意思完全不同：前者再等等就有了，
// 后者说明主控那时停着或者那一分钟被跳过了。
func (s *Store) EarliestTrafficSample(ctx context.Context) (time.Time, bool, error) {
	var at time.Time
	err := s.Pool.QueryRow(ctx, `SELECT min(at) FROM traffic_samples`).Scan(&at)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return at, false, nil
		}
		// min() 在空表上回 NULL，Scan 进 time.Time 会报错而不是 ErrNoRows。
		var nullable *time.Time
		if e2 := s.Pool.QueryRow(ctx, `SELECT min(at) FROM traffic_samples`).Scan(&nullable); e2 == nil {
			return at, nullable != nil, nil
		}
		return at, false, err
	}
	return at, true, nil
}
