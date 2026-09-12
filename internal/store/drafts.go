package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	// **草稿必须是一个对象，写入时就拒。**
	//
	// 这里原先把 Unmarshal 的 err 丢掉，于是 `[1,2]` / `"x"` / `123` / `null`
	// 这些合法 JSON 但不是对象的东西照样入库——jsonb 列只要求合法 JSON。
	//
	// 代价不在这一步：之后 deploy 的 mergeInto 把它 Unmarshal 进 map 会失败，
	// Deploy 与 Preview 双双 500，**而人在界面上找不到入口删它**（issue #58）。
	// 一个从界面上解不开的死局，而它本可以在入口处就被挡住。
	//
	// 草稿按 CONTEXT.md 的定义就是 Partial（对象）。
	m, err := asObject(patch)
	if err != nil {
		return fmt.Errorf("草稿 %s: %w", resKey, err)
	}
	if len(m) == 0 {
		// 空对象是**撤回**：最后一处改动被去掉了，这条草稿就不该存在。
		return s.DeleteDraft(ctx, resKey)
	}
	return putDraft(ctx, s.Pool, resKey, patch, by)
}

// PutDrafts 把一批草稿**一次性**写进去：要么全在，要么一条都不写。
//
// 回滚是它唯一的调用方，而回滚原先是逐条写、中途失败就地返回——工作台里
// 于是亮着前几条，接口回 500，响应里一个 res_key 都不报。人接着按「待下发」
// 发出去的是半个回滚（issue #44）。
//
// 这里不走 PutDraft 里那条「空对象等于删除」的捷径：回滚写的是快照与 live
// 的差异，空差异本来就不会进这张表。
func (s *Store) PutDrafts(ctx context.Context, patches map[string]json.RawMessage, by string) error {
	if len(patches) == 0 {
		return nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// **按键排序，不靠 map 的随机顺序。**
	//
	// 顺序在正确的实现里无所谓——整批要么全成要么全不成。它要紧是因为
	// 「整批」这件事只有在「失败之前确实已经写了几条」时才检验得出来：
	// 随机顺序下非法的那条可能排在最前，于是一个逐条写的坏实现也什么都
	// 没留下，测试照样绿。探针把这件事抓了出来。
	keys := make([]string, 0, len(patches))
	for k := range patches {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, resKey := range keys {
		if err := putDraft(ctx, tx, resKey, patches[resKey], by); err != nil {
			return fmt.Errorf("写回草稿 %s: %w", resKey, err)
		}
	}
	return tx.Commit(ctx)
}

func putDraft(ctx context.Context, q querier, resKey string, patch json.RawMessage, by string) error {
	_, err := q.Exec(ctx,
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
	return deleteDrafts(ctx, s.Pool, resKeys)
}

// deleteDrafts 是 DeleteDrafts 的事务内版本（同一条 SQL 只有这一份）。
func deleteDrafts(ctx context.Context, q querier, resKeys []string) error {
	if len(resKeys) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `DELETE FROM config_drafts WHERE res_key = ANY($1)`, resKeys)
	return err
}

func (s *Store) DeleteAllDrafts(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM config_drafts`)
	return err
}

// asObject 把一份 patch 解成对象，不是对象就报错。
//
// **一道闸，不是两道。** 原先这里是「Unmarshal 失败」与「解出来是 nil」两个
// 分支，而它们对同一批输入互为兜底：单独破坏任何一个，另一个还接着，
// 测试照样绿——探针把这件事抓了出来（domain.md「互为兜底的两层，让单点破坏
// 证明不了任何事」）。
//
// `null` 是那两个分支重叠的地方：它能解进 map[string]any 而不报错，得到 nil，
// 而 len(nil) 也是 0 —— 混进「空对象等于撤回」那条捷径里就成了一次静默删除。
func asObject(patch json.RawMessage) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(patch, &m); err != nil {
		return nil, fmt.Errorf("不是一个对象：%w", err)
	}
	if m == nil {
		return nil, errors.New("不能是 null")
	}
	return m, nil
}
