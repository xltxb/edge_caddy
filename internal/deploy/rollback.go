package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xltxb/edge_caddy/internal/model"
)

// Rollback 把某个历史版本的资源状态写回**草稿**，返回写回的资源键。
//
// 不直接推送（PRD §6.3）。回滚往往发生在出事的时候，正是最需要有人看一眼
// diff 的时刻——直接推送等于在最紧张的时刻绕过校验与人工确认。
//
// 只写回**有差异**的资源：把没变的也写成草稿，用户会在确认弹层看到一堆
// 「改动」却全是空 diff，分不清哪些是真要回滚的。
func (o *Orchestrator) Rollback(ctx context.Context, cfgVersion, operator string) ([]string, error) {
	baseline, err := o.st.Baseline(ctx)
	if err != nil {
		return nil, err
	}
	if cfgVersion == baseline {
		return nil, errors.New("该版本就是当前基线，无需回滚")
	}

	d, err := o.st.GetDeployByVersion(ctx, cfgVersion)
	if err != nil {
		return nil, fmt.Errorf("查询版本 %s: %w", cfgVersion, err)
	}
	if len(d.Snapshot.Routes) == 0 {
		// 早期版本的快照可能只有渲染产物。渲染是有损的，反推不回资源模型，
		// 因此明确拒绝而不是尽力而为——「尽力而为」的结果是回滚出一份
		// 与当年不同的配置，而没人会发现。
		return nil, fmt.Errorf("版本 %s 的快照不含资源模型，无法回滚", cfgVersion)
	}

	live, err := o.st.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	byDomain := map[string]model.Route{}
	for _, r := range live {
		byDomain[r.Domain] = r
	}

	written := make([]string, 0, len(d.Snapshot.Routes))
	now := time.Now()
	for _, old := range d.Snapshot.Routes {
		cur, exists := byDomain[old.Domain]
		if exists && sameRoute(cur, old) {
			continue // 没变过，不写草稿
		}
		patch, err := routePatch(old)
		if err != nil {
			return nil, err
		}
		key := "route:" + old.Domain
		if err := o.st.PutDraft(ctx, key, patch, operator, now); err != nil {
			return nil, err
		}
		written = append(written, key)
	}
	o.log.Info("回滚已写回草稿", "operator", operator, "cfg_version", cfgVersion, "resources", len(written))
	return written, nil
}

// sameRoute 比较两条路由是否实质相同（白名单先规范化，与草稿语义一致）。
func sameRoute(a, b model.Route) bool {
	a.Version, b.Version = 0, 0
	a.Whitelist = normalizeWL(a.Whitelist)
	b.Whitelist = normalizeWL(b.Whitelist)
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func normalizeWL(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := trimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	isSpace := func(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

// routePatch 把一条历史路由转成草稿 patch。
//
// 走 JSON 往返，与 applyPatch 同源：加字段时两边同时生效，不会出现
// 「回滚漏了某个字段」这种只在特定字段上发生、极难察觉的缺陷。
func routePatch(r model.Route) (map[string]any, error) {
	blob, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil, err
	}
	// 版本号由下发推进，不属于回滚内容
	delete(m, "ver")
	return m, nil
}
