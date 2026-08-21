package store

import (
	"context"
	"time"
)

// NodeLogLine 是节点上报的一行运行日志。
type NodeLogLine struct {
	At    time.Time `json:"at"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}

// nodeLogKeep 是每个节点保留的条数。
//
// 查询上限是 200（契约 §4），这里留 2.5 倍余量让人能往回翻。
// 再多没有意义：节点日志是「刚才发生了什么」的答案，不是审计——
// 要追溯久远的事，看审计日志与事件流，那两样是主控自己写的，不会随节点消失。
const nodeLogKeep = 500

// AppendNodeLogs 追加一批日志，并把这个节点的历史裁到 nodeLogKeep 条。
//
// 裁剪跟写入放在同一个事务里：分开的话，一个「只写不裁」的失败会让表
// 无声地长下去——而它跟正常工作长得一模一样，直到磁盘满。
func (s *Store) AppendNodeLogs(ctx context.Context, nodeID string, lines []NodeLogLine) error {
	if len(lines) == 0 {
		return nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, l := range lines {
		if _, err := tx.Exec(ctx,
			`INSERT INTO node_logs (node_id, at, level, msg) VALUES ($1,$2,$3,$4)`,
			nodeID, l.At, l.Level, l.Msg); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM node_logs WHERE node_id = $1 AND id NOT IN (
		   SELECT id FROM node_logs WHERE node_id = $1 ORDER BY id DESC LIMIT $2)`,
		nodeID, nodeLogKeep); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListNodeLogs 读某个节点最近的 limit 条，**倒序**（契约 §4）。
func (s *Store) ListNodeLogs(ctx context.Context, nodeID string, limit int) ([]NodeLogLine, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT at, level, msg FROM node_logs
		 WHERE node_id = $1 ORDER BY id DESC LIMIT $2`, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []NodeLogLine{}
	for rows.Next() {
		var l NodeLogLine
		if err := rows.Scan(&l.At, &l.Level, &l.Msg); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
