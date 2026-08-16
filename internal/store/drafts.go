package store

import (
	"context"
	"fmt"
	"time"

	"github.com/xltxb/edge_caddy/internal/model"
)

// ListDrafts 返回全部草稿。
//
// 不按操作人过滤：草稿是全局可见的，你要能看到 ops-bot 半夜改了什么。
// 「谁改的」体现在 UpdatedBy 上，供确认弹层逐条标注作者。
func (s *Store) ListDrafts(ctx context.Context) ([]model.Draft, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT res_key, patch, updated_by, updated_at FROM config_drafts ORDER BY res_key`)
	if err != nil {
		return nil, fmt.Errorf("查询草稿: %w", err)
	}
	defer rows.Close()

	out := make([]model.Draft, 0, 8)
	for rows.Next() {
		var (
			d     model.Draft
			patch string
			at    string
		)
		if err := rows.Scan(&d.ResKey, &patch, &d.UpdatedBy, &at); err != nil {
			return nil, fmt.Errorf("读取草稿行: %w", err)
		}
		if err := decodeJSON(patch, &d.Patch); err != nil {
			return nil, fmt.Errorf("草稿 %s: %w", d.ResKey, err)
		}
		d.UpdatedAt = decodeTime(at)
		out = append(out, d)
	}
	return out, rows.Err()
}

// PutDraft 写入一条草稿。patch 为空表示该资源已无待下发改动，直接删除该行。
//
// 「改回原值就删掉草稿键」是设计稿定下的语义（sameVal / normWL）：用户把
// 白名单里的换行删了又加回来，值其实没变，不该在待下发列表里留一条幽灵改动。
// 判断本身在调用方做——存储层只负责「空 patch 等于没有草稿」。
func (s *Store) PutDraft(ctx context.Context, key string, patch map[string]any, by string, now time.Time) error {
	if len(patch) == 0 {
		_, err := s.db.ExecContext(ctx, `DELETE FROM config_drafts WHERE res_key = ?`, key)
		if err != nil {
			return fmt.Errorf("删除草稿 %s: %w", key, err)
		}
		return nil
	}
	blob, err := encodeJSON(patch)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO config_drafts (res_key, patch, updated_by, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(res_key) DO UPDATE SET
			patch=excluded.patch, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		key, blob, by, encodeTime(now))
	if err != nil {
		return fmt.Errorf("写入草稿 %s: %w", key, err)
	}
	return nil
}

// DeleteDrafts 清除指定草稿键。下发成功后只清**本次勾选**的那些，
// 未勾选的草稿必须原样留着——它们是别人还没推的改动。
func (s *Store) DeleteDrafts(ctx context.Context, keys []string) error {
	for _, k := range keys {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM config_drafts WHERE res_key = ?`, k); err != nil {
			return fmt.Errorf("清除草稿 %s: %w", k, err)
		}
	}
	return nil
}
