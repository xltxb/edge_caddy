package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/secret"
)

// ListRules 读出访问规则。
//
// sealer 用来解开服务密钥的共享密钥。它作为参数而不是 Store 的字段：
// 谁需要解密在调用处一眼可见，而大多数读取（比如 GET /rules 给前端）
// **不该**解密——凭证只写入不回显（PRD §7）。传 nil 即不解密。
func (s *Store) ListRules(ctx context.Context, sealer *secret.Sealer) ([]model.Rule, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, name, type::text, enabled, spec, apply_to, version, secret_sealed
		 FROM access_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Rule
	for rows.Next() {
		var r model.Rule
		var spec, applyTo, sealed []byte
		if err := rows.Scan(&r.ID, &r.Name, &r.Type, &r.Enabled,
			&spec, &applyTo, &r.Version, &sealed); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(spec, &r.Spec); err != nil {
			return nil, fmt.Errorf("解析规则 %s 的 spec: %w", r.ID, err)
		}
		if err := json.Unmarshal(applyTo, &r.ApplyTo); err != nil {
			return nil, fmt.Errorf("解析规则 %s 的 apply_to: %w", r.ID, err)
		}
		r.Spec.SecretConfigured = len(sealed) > 0
		if sealer != nil && len(sealed) > 0 {
			plain, err := sealer.Open(sealed)
			if err != nil {
				return nil, fmt.Errorf("解开规则 %s 的共享密钥: %w", r.ID, err)
			}
			r.Secret = string(plain)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRule(ctx context.Context, id string) (model.Rule, error) {
	rules, err := s.ListRules(ctx, nil)
	if err != nil {
		return model.Rule{}, err
	}
	for _, r := range rules {
		if r.ID == id {
			return r, nil
		}
	}
	return model.Rule{}, ErrNotFound
}

// UpsertRule 写入规则。secret 为空串表示**保持不变**——
// 前端不回显凭证，因此它提交时也带不出原值来（PRD §7）。
func (s *Store) UpsertRule(ctx context.Context, r model.Rule, plainSecret string, sealer *secret.Sealer) error {
	spec, err := json.Marshal(r.Spec)
	if err != nil {
		return err
	}
	applyTo, err := json.Marshal(defaultSlice(r.ApplyTo))
	if err != nil {
		return err
	}

	var sealed any
	if plainSecret != "" {
		if sealer == nil {
			return errors.New("要写入共享密钥但没有可用的密封器")
		}
		b, err := sealer.Seal([]byte(plainSecret))
		if err != nil {
			return err
		}
		sealed = b
	}

	_, err = s.Pool.Exec(ctx,
		`INSERT INTO access_rules (id, name, type, enabled, spec, apply_to, secret_sealed)
		 VALUES ($1,$2,$3::rule_type,$4,$5,$6,$7)
		 ON CONFLICT (id) DO UPDATE SET
		   name = EXCLUDED.name, type = EXCLUDED.type, enabled = EXCLUDED.enabled,
		   spec = EXCLUDED.spec, apply_to = EXCLUDED.apply_to,
		   secret_sealed = COALESCE(EXCLUDED.secret_sealed, access_rules.secret_sealed)`,
		r.ID, r.Name, r.Type, r.Enabled, spec, applyTo, sealed)
	return err
}

func (s *Store) BumpRuleVersions(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE access_rules SET version = version + 1 WHERE id = ANY($1)`, ids)
	return err
}

// UnbindDomain 把一个域名从所有规则的 apply_to 里摘掉。
// 删除路由时联动调用——留着一条指向已删域名的绑定，会让人以为那个域名还受保护。
func (s *Store) UnbindDomain(ctx context.Context, domain string) ([]string, error) {
	rows, err := s.Pool.Query(ctx,
		`UPDATE access_rules
		 SET apply_to = apply_to - $1
		 WHERE apply_to ? $1
		 RETURNING id`, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
