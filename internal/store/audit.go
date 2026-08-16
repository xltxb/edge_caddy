package store

import (
	"context"
	"fmt"

	"github.com/xltxb/edge_caddy/internal/model"
)

// AppendAudit 追加一条审计记录。
func (s *Store) AppendAudit(ctx context.Context, a model.AuditLog) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_logs (operator, action, target, src_ip, result, detail, at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.Operator, a.Action, a.Target, a.SrcIP, a.Result, a.Detail, encodeTime(a.At))
	if err != nil {
		return fmt.Errorf("写入审计: %w", err)
	}
	return nil
}

// ListAudit 倒序返回审计记录。operator 为空表示不筛选。
func (s *Store) ListAudit(ctx context.Context, operator string, limit int) ([]model.AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id, operator, action, target, src_ip, result, detail, at FROM audit_logs`
	args := []any{}
	if operator != "" {
		q += ` WHERE operator = ?`
		args = append(args, operator)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("查询审计: %w", err)
	}
	defer rows.Close()

	out := make([]model.AuditLog, 0, limit)
	for rows.Next() {
		var (
			a  model.AuditLog
			at string
		)
		if err := rows.Scan(&a.ID, &a.Operator, &a.Action, &a.Target,
			&a.SrcIP, &a.Result, &a.Detail, &at); err != nil {
			return nil, fmt.Errorf("读取审计行: %w", err)
		}
		a.At = decodeTime(at)
		out = append(out, a)
	}
	return out, rows.Err()
}
