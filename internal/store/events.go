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
