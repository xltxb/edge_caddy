package store

import (
	"context"
	"fmt"

	"github.com/xltxb/edge_caddy/internal/model"
)

// ListWeights 返回某个域名的全部解析权重。
func (s *Store) ListWeights(ctx context.Context, domain string) ([]model.DNSWeight, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT domain, node_id, line, weight FROM dns_weights WHERE domain = ?
		 ORDER BY line, node_id`, domain)
	if err != nil {
		return nil, fmt.Errorf("读取解析权重: %w", err)
	}
	defer rows.Close()

	var out []model.DNSWeight
	for rows.Next() {
		var w model.DNSWeight
		if err := rows.Scan(&w.Domain, &w.NodeID, &w.Line, &w.Weight); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// PutWeights 整体替换某个域名的权重。
//
// 整体替换而不是逐条更新：界面上是一次性提交整张表，逐条更新会在「删掉一个
// 节点」这种情形下留下孤儿行，而孤儿行会让那台已经下架的机器继续参与解析。
func (s *Store) PutWeights(ctx context.Context, domain string, ws []model.DNSWeight) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM dns_weights WHERE domain = ?`, domain); err != nil {
		return fmt.Errorf("清理旧权重: %w", err)
	}
	for _, w := range ws {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO dns_weights (domain, node_id, line, weight) VALUES (?, ?, ?, ?)`,
			domain, w.NodeID, w.Line, w.Weight); err != nil {
			return fmt.Errorf("写入 %s 在 %s 上的权重: %w", w.NodeID, w.Line, err)
		}
	}
	return tx.Commit()
}
