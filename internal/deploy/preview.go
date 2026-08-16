package deploy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
)

// Preview 返回下发前后的两份**权威渲染**。
//
// 两侧都由这里的渲染器产出（docs/adr/0007）：工作台右栏那份可读表示不是下发
// 内容，只有这里的字节才是。任何一侧改由前端渲染，「所见即所发」就立不住了。
//
// resKeys 是本次勾选的资源；未勾选的草稿**不进入** next——用户批准的必须
// 就是他实际推出去的那份。
func (o *Orchestrator) Preview(ctx context.Context, resKeys []string) (current, next string, err error) {
	live, err := o.st.ListRoutes(ctx)
	if err != nil {
		return "", "", fmt.Errorf("读取路由: %w", err)
	}
	curBlob, err := render.CaddyWith(live, o.opts)
	if err != nil {
		return "", "", fmt.Errorf("渲染当前配置失败：%w", err)
	}

	merged, err := o.mergeDrafts(ctx, live, resKeys)
	if err != nil {
		return "", "", err
	}
	nextBlob, err := render.CaddyWith(merged, o.opts)
	if err != nil {
		return "", "", fmt.Errorf("渲染合入草稿后的配置失败：%w", err)
	}
	return string(curBlob), string(nextBlob), nil
}

// mergeDrafts 把选中的草稿叠加到线上值上。
func (o *Orchestrator) mergeDrafts(ctx context.Context, live []model.Route, resKeys []string) ([]model.Route, error) {
	drafts, err := o.st.ListDrafts(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取草稿: %w", err)
	}
	selected := map[string]bool{}
	for _, k := range resKeys {
		selected[k] = true
	}
	byKey := map[string]map[string]any{}
	for _, d := range drafts {
		if len(resKeys) == 0 || selected[d.ResKey] {
			byKey[d.ResKey] = d.Patch
		}
	}

	out := make([]model.Route, 0, len(live))
	for _, r := range live {
		patch, has := byKey["route:"+r.Domain]
		if !has {
			out = append(out, r)
			continue
		}
		merged, err := applyPatch(r, patch)
		if err != nil {
			return nil, fmt.Errorf("路由 %s 的草稿: %w", r.Domain, err)
		}
		out = append(out, merged)
	}
	return out, nil
}

// applyPatch 把 Partial 叠加到一条路由上。
//
// 走 JSON 往返而不是逐字段 switch：加字段时不必记得来这里补一处，
// 而「忘了补」的表现是那个字段的草稿静默失效——改了、看着像生效、推上去没变。
func applyPatch(base model.Route, patch map[string]any) (model.Route, error) {
	blob, err := json.Marshal(base)
	if err != nil {
		return base, err
	}
	var m map[string]any
	if err := json.Unmarshal(blob, &m); err != nil {
		return base, err
	}
	for k, v := range patch {
		m[k] = v
	}
	merged, err := json.Marshal(m)
	if err != nil {
		return base, err
	}
	var out model.Route
	if err := json.Unmarshal(merged, &out); err != nil {
		return base, err
	}
	return out, nil
}
