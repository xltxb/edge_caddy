package store

import (
	"context"
	"time"
)

// Event 是事件流里的一条。Kind 四档：
// ok = 成功完成的动作，info = 流水账，warn，crit。
// ok 与 info 合并会让下发成功和背景噪音同色（api-contract §2）。
type Event struct {
	ID        int64     `json:"id"`
	Node      string    `json:"node"`
	Kind      string    `json:"kind"`
	Msg       string    `json:"msg"`
	CreatedAt time.Time `json:"at"`
}

// 隧道生命周期的两个事件文案。**它们必须是常量**：
// CountReconnects 要按文案去数，而一个写在两处的字符串迟早会分叉——
// 分叉之后计数悄悄变成 0，而 0 跟「这条隧道很稳」长得一模一样。
const (
	// EventNodeJoined 是**凭 Token 的首次加入**。一台机器一生只该有几条。
	EventNodeJoined = "节点已接入"
	// EventTunnelReconnected 是**同一台机器的隧道断了又接上**。
	//
	// 这两件事此前记的是同一句话，于是事件流读起来是「这台机器半小时内
	// 重新加入了三次集群」，而实际是一条隧道断了两次。
	EventTunnelReconnected = "隧道已重连"
)

// CountReconnects 数一个节点最近一段时间里重连了几次。
//
// **它存在的理由是去抖会把真故障吃掉。** 隧道断开到重连只要 1–2 秒，
// 而离线判定要连续错过 heartbeat_interval × offline_threshold（默认 9 秒）
// 才翻 down —— 所以一条每十分钟断一次的隧道，在所有徽标上都是健康的。
//
// 契约 §4 那句「短暂不一致是正常的，那是判定的去抖窗口」是对的，
// 而它的另一面就是这个：**去抖分不出「一次抖动」和「反复抖动」**。
// 前者不该惊动人，后者是故障。区分它们需要的不是更灵敏的判定，
// 是**一个跨时间的计数**。
func (s *Store) CountReconnects(ctx context.Context, nodeID string, within time.Duration) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM events
		  WHERE node_id = $1 AND msg = $2 AND created_at > now() - $3::interval`,
		nodeID, EventTunnelReconnected, within.String()).Scan(&n)
	return n, err
}

func (s *Store) InsertEvent(ctx context.Context, node, kind, msg string) (Event, error) {
	var e Event
	var nodeArg any
	if node != "" {
		nodeArg = node
	}
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO events (node_id, kind, msg) VALUES ($1,$2::event_kind,$3)
		 RETURNING id, coalesce(node_id,''), kind::text, msg, created_at`,
		nodeArg, kind, msg).Scan(&e.ID, &e.Node, &e.Kind, &e.Msg, &e.CreatedAt)
	return e, err
}

func (s *Store) RecentEvents(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 || limit > 200 {
		limit = 40
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT id, coalesce(node_id,''), kind::text, msg, created_at
		 FROM events ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Node, &e.Kind, &e.Msg, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
