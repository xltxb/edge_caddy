package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/xltxb/edge_caddy/internal/model"
)

// 全局策略只有固定的两条。它们的 spec 字段清单以高保真设计稿为准，
// 因此库里存原始 JSON，不在 Go 里定义结构体——写死在两处只会让两边同时改。
var defaultPolicies = []model.Policy{
	{ID: model.PolicyTLS, Name: "TLS 策略", Spec: json.RawMessage(`{}`)},
	{ID: model.PolicyLog, Name: "日志与限流", Spec: json.RawMessage(`{}`)},
}

// ListPolicies 读出两条全局策略。
//
// 库里没有就返回默认的空壳，而不是空列表：这两条策略在概念上**始终存在**
// （资源树里那两栏不该因为没人改过就消失）。
func (s *Store) ListPolicies(ctx context.Context) ([]model.Policy, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, name, spec, version FROM global_policies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := map[string]model.Policy{}
	for rows.Next() {
		var p model.Policy
		if err := rows.Scan(&p.ID, &p.Name, &p.Spec, &p.Version); err != nil {
			return nil, err
		}
		byID[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]model.Policy, 0, len(defaultPolicies))
	for _, d := range defaultPolicies {
		if p, ok := byID[d.ID]; ok {
			out = append(out, p)
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Store) GetPolicy(ctx context.Context, id string) (model.Policy, error) {
	var p model.Policy
	err := s.Pool.QueryRow(ctx,
		`SELECT id, name, spec, version FROM global_policies WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.Spec, &p.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		for _, d := range defaultPolicies {
			if d.ID == id {
				return d, nil
			}
		}
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) UpsertPolicy(ctx context.Context, p model.Policy) error {
	name := p.Name
	if name == "" {
		for _, d := range defaultPolicies {
			if d.ID == p.ID {
				name = d.Name
			}
		}
	}
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO global_policies (id, name, spec) VALUES ($1,$2,$3)
		 ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, spec = EXCLUDED.spec`,
		p.ID, name, p.Spec)
	return err
}

func (s *Store) BumpPolicyVersions(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE global_policies SET version = version + 1 WHERE id = ANY($1)`, ids)
	return err
}

// IsPolicyID 说明一个 id 是不是合法的全局策略。
func IsPolicyID(id string) bool {
	for _, d := range defaultPolicies {
		if d.ID == id {
			return true
		}
	}
	return false
}

func (s *Store) DeleteRoute(ctx context.Context, domain string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM proxy_routes WHERE domain = $1`, domain)
	return err
}
