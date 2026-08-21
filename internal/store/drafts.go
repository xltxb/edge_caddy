package store

import (
	"context"
	"encoding/json"
	"time"
)

// Draft 是一份尚未下发的改动，叠加在基线之上（CONTEXT.md「草稿」）。
// 草稿**全局可见**——任何人都能看到别人正在改什么。
type Draft struct {
	ResKey    string          `json:"res_key"`
	Patch     json.RawMessage `json:"patch"`
	UpdatedBy string          `json:"updated_by"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func (s *Store) ListDrafts(ctx context.Context) ([]Draft, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT res_key, patch, coalesce(updated_by,''), updated_at
		 FROM config_drafts ORDER BY res_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Draft
	for rows.Next() {
		var d Draft
		if err := rows.Scan(&d.ResKey, &d.Patch, &d.UpdatedBy, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PutDraft 写入一个 Partial。
//
// **空对象等价于删除**：字段值改回与线上一致时前端会把键从 Partial 里去掉，
// 去到最后一个键时留下的就是空对象。留着一行空草稿会让「有几处未下发改动」
// 这个数字虚报，而那个数字正是工作台上蓝点的依据。
func (s *Store) PutDraft(ctx context.Context, resKey string, patch json.RawMessage, by string) error {
	var m map[string]any
	if err := json.Unmarshal(patch, &m); err == nil && len(m) == 0 {
		return s.DeleteDraft(ctx, resKey)
	}
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO config_drafts (res_key, patch, updated_by, updated_at)
		 VALUES ($1,$2,$3,now())
		 ON CONFLICT (res_key) DO UPDATE SET
		   patch = EXCLUDED.patch, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		resKey, patch, by)
	return err
}

func (s *Store) DeleteDraft(ctx context.Context, resKey string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM config_drafts WHERE res_key = $1`, resKey)
	return err
}

func (s *Store) DeleteDrafts(ctx context.Context, resKeys []string) error {
	if len(resKeys) == 0 {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM config_drafts WHERE res_key = ANY($1)`, resKeys)
	return err
}

func (s *Store) DeleteAllDrafts(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM config_drafts`)
	return err
}
