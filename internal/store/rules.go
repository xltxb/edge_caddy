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
	return upsertRule(ctx, s.Pool, r, plainSecret, sealer)
}

// upsertRule 是 UpsertRule 的事务内版本（同一条 SQL 只有这一份）。
// ruleColumns 把一条规则备成可以直接塞进 SQL 的几列。
//
// 抽出来是因为 upsertRule 与 InsertRuleIfAbsent 只差 ON CONFLICT 那一句，
// 而密钥密封那一段抄第二份的话，两处迟早对密钥的处置不一致——
// 那种不一致的症状是「保存成功，而校验端点验不过」。
func ruleColumns(r model.Rule, plainSecret string, sealer *secret.Sealer) (spec, applyTo []byte, sealed any, err error) {
	spec, err = json.Marshal(r.Spec)
	if err != nil {
		return nil, nil, nil, err
	}
	applyTo, err = json.Marshal(defaultSlice(r.ApplyTo))
	if err != nil {
		return nil, nil, nil, err
	}
	if plainSecret != "" {
		if sealer == nil {
			return nil, nil, nil, errors.New("要写入共享密钥但没有可用的密封器")
		}
		b, serr := sealer.Seal([]byte(plainSecret))
		if serr != nil {
			return nil, nil, nil, serr
		}
		sealed = b
	}
	return spec, applyTo, sealed, nil
}

func upsertRule(ctx context.Context, q querier, r model.Rule, plainSecret string, sealer *secret.Sealer) error {
	spec, applyTo, sealed, err := ruleColumns(r, plainSecret, sealer)
	if err != nil {
		return err
	}

	_, err = q.Exec(ctx,
		`INSERT INTO access_rules (id, name, type, enabled, spec, apply_to, secret_sealed)
		 VALUES ($1,$2,$3::rule_type,$4,$5,$6,$7)
		 ON CONFLICT (id) DO UPDATE SET
		   name = EXCLUDED.name, type = EXCLUDED.type, enabled = EXCLUDED.enabled,
		   spec = EXCLUDED.spec, apply_to = EXCLUDED.apply_to,
		   secret_sealed = COALESCE(EXCLUDED.secret_sealed, access_rules.secret_sealed)`,
		r.ID, r.Name, r.Type, r.Enabled, spec, applyTo, sealed)
	return err
}

// InsertRuleIfAbsent 只在这个 id 还没被占时写入，回 true 表示真的建了。
//
// **「只建不覆盖」必须由数据库来判，不能先查再写。** handler 原先是
// GetRule 查一遍、没有就 UpsertRule，两句之间有窗口：两个人同时新建同一个
// id 时两句查询都说没有，然后两次写都落地，后写的把先写的整个换掉还回
// code: 0——那正是 #70 要挡的「静默覆盖别人配好的规则」本身。
//
// 契约 §6.2 承诺的是「撞上已有 id 时一个字节都不写」，而那是一句关于
// 原子性的话，靠两条语句兑现不了。由 TestInsertRuleIfAbsentIsAtomic 守着。
func (s *Store) InsertRuleIfAbsent(ctx context.Context, r model.Rule, plainSecret string, sealer *secret.Sealer) (bool, error) {
	spec, applyTo, sealed, err := ruleColumns(r, plainSecret, sealer)
	if err != nil {
		return false, err
	}
	tag, err := s.Pool.Exec(ctx,
		`INSERT INTO access_rules (id, name, type, enabled, spec, apply_to, secret_sealed)
		 VALUES ($1,$2,$3::rule_type,$4,$5,$6,$7)
		 ON CONFLICT (id) DO NOTHING`,
		r.ID, r.Name, r.Type, r.Enabled, spec, applyTo, sealed)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
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

// DeleteRule 删掉一条访问规则。
//
// 规则**没有外键指向它**（apply_to 是域名数组，方向是规则→域名），
// 所以删除不会留下悬挂引用——不必像删路由那样先解绑。
func (s *Store) DeleteRule(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM access_rules WHERE id = $1`, id)
	return err
}
