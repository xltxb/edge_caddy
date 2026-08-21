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
