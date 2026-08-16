package store

import (
	"context"
	"fmt"

	"github.com/xltxb/edge_caddy/internal/model"
)

func (s *Store) ListRules(ctx context.Context) ([]model.AccessRule, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, type, enabled, spec, apply_to, version FROM access_rules ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("查询访问规则: %w", err)
	}
	defer rows.Close()

	out := make([]model.AccessRule, 0, 8)
	for rows.Next() {
		var (
			r             model.AccessRule
			enabled       int
			spec, applyTo string
		)
		if err := rows.Scan(&r.ID, &r.Name, &r.Type, &enabled, &spec, &applyTo, &r.Version); err != nil {
			return nil, fmt.Errorf("读取规则行: %w", err)
		}
		r.Enabled = enabled == 1
		if err := decodeJSON(spec, &r.Spec); err != nil {
			return nil, fmt.Errorf("规则 %s 的 spec: %w", r.ID, err)
		}
		if err := decodeJSON(applyTo, &r.ApplyTo); err != nil {
			return nil, fmt.Errorf("规则 %s 的绑定域名: %w", r.ID, err)
		}
		if r.ApplyTo == nil {
			r.ApplyTo = []string{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) PutRule(ctx context.Context, r model.AccessRule) error {
	spec, err := encodeJSON(r.Spec)
	if err != nil {
		return err
	}
	applyTo, err := encodeJSON(nonNil(r.ApplyTo))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO access_rules (id, name, type, enabled, spec, apply_to, version)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, type=excluded.type, enabled=excluded.enabled,
			spec=excluded.spec, apply_to=excluded.apply_to`,
		r.ID, r.Name, string(r.Type), boolToInt(r.Enabled), spec, applyTo, r.Version)
	if err != nil {
		return fmt.Errorf("写入规则 %s: %w", r.ID, err)
	}
	return nil
}

func (s *Store) DeleteRule(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM access_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除规则 %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
