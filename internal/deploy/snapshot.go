package deploy

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/xltxb/edge_caddy/internal/model"
)

// Snapshot 是一次下发所固化的**资源状态**，回滚以它为源（CONTEXT.md「快照」）。
//
// 存资源状态而不是渲染后的 Caddy JSON：回滚要「逐资源比对差异并写回草稿」
// （后端文档 §6），而渲染产物是把全部资源揉成一份配置之后的样子，
// 没法从里面拆回「哪条路由的哪个字段变了」。
//
// **不含共享密钥**：model.Rule.Secret 不参与 JSON 序列化。快照进的是 JSONB 列，
// 把明文密钥复制一份进去，等于在一个不加密的地方多留一份凭证。
// 代价是回滚不恢复旧密钥——而那是对的：密钥只写入不回显（PRD §7），
// 它本来就不是用户在 diff 里看到的东西。
type Snapshot struct {
	Routes []model.Route `json:"routes"`
	Rules  []model.Rule  `json:"rules"`
}

// Skipped 是回滚覆盖不到的资源，连同原因一起回给调用方。
//
// 静默跳过是不可接受的：人点了「回滚到 cfg-8b03e7」，界面说成功了，
// 而某条路由其实没回去——那比明说「这条恢复不了」糟糕得多。
type Skipped struct {
	ResKey string `json:"res_key"`
	Reason string `json:"reason"`
}

// diffToDrafts 比对快照与当前 live，产出要写回草稿的 Partial。
//
// 只产出**有差异的字段**：把整个资源原样写回草稿会让工作台上每个字段都亮成
// 改动过，而其中大部分与线上一致——那样 diff 就失去了指出「哪儿变了」的作用。
func diffToDrafts(snap Snapshot, liveRoutes []model.Route, liveRules []model.Rule) (map[string]json.RawMessage, []Skipped, error) {
	patches := map[string]json.RawMessage{}
	var skipped []Skipped

	liveRouteByDomain := map[string]model.Route{}
	for _, r := range liveRoutes {
		liveRouteByDomain[r.Domain] = r
	}
	liveRuleByID := map[string]model.Rule{}
	for _, r := range liveRules {
		liveRuleByID[r.ID] = r
	}

	snapRouteDomains := map[string]bool{}
	for _, want := range snap.Routes {
		snapRouteDomains[want.Domain] = true
		cur, ok := liveRouteByDomain[want.Domain]
		if !ok {
			// 草稿是叠加在 live 行上的 Partial，没有那一行就无处可叠。
			// 而回滚承诺「只写草稿、不动线上」——为了恢复它去直接写 live，
			// 就破坏了这个承诺。
			skipped = append(skipped, Skipped{
				ResKey: "route:" + want.Domain,
				Reason: "这条路由此后被删除了，回滚不会把它建回来——请手动新建",
			})
			continue
		}
		patch, err := fieldDiff(cur, want)
		if err != nil {
			return nil, nil, err
		}
		if len(patch) > 0 {
			patches["route:"+want.Domain] = patch
		}
	}
	for _, cur := range liveRoutes {
		if !snapRouteDomains[cur.Domain] {
			skipped = append(skipped, Skipped{
				ResKey: "route:" + cur.Domain,
				Reason: "这条路由是那次下发之后才新建的，回滚不会删除它",
			})
		}
	}

	snapRuleIDs := map[string]bool{}
	for _, want := range snap.Rules {
		snapRuleIDs[want.ID] = true
		cur, ok := liveRuleByID[want.ID]
		if !ok {
			skipped = append(skipped, Skipped{
				ResKey: "rule:" + want.ID,
				Reason: "这条访问规则此后被删除了，回滚不会把它建回来——请手动新建",
			})
			continue
		}
		patch, err := fieldDiff(cur, want)
		if err != nil {
			return nil, nil, err
		}
		if len(patch) > 0 {
			patches["rule:"+want.ID] = patch
		}
	}
	for _, cur := range liveRules {
		if !snapRuleIDs[cur.ID] {
			skipped = append(skipped, Skipped{
				ResKey: "rule:" + cur.ID,
				Reason: "这条访问规则是那次下发之后才新建的，回滚不会删除它",
			})
		}
	}

	return patches, skipped, nil
}

// fieldDiff 返回 want 相对 cur 有差异的那些字段。
//
// 走 JSON 往返比对而不是逐字段 if：字段会增加，逐字段的那个函数每次都要跟着改，
// 而漏掉一个字段的症状是「回滚了但那一项没回去」——静默且难查。
func fieldDiff[T any](cur, want T) (json.RawMessage, error) {
	curMap, err := toMap(cur)
	if err != nil {
		return nil, err
	}
	wantMap, err := toMap(want)
	if err != nil {
		return nil, err
	}

	patch := map[string]json.RawMessage{}
	for k, wv := range wantMap {
		// version 是系统维护的，不是人配的：把它写进草稿会让工作台显示
		// 一条谁也没改过的「变更」。
		if k == "version" {
			continue
		}
		cv, ok := curMap[k]
		if !ok || !jsonEqual(cv, wv) {
			patch[k] = wv
		}
	}
	if len(patch) == 0 {
		return nil, nil
	}
	return json.Marshal(patch)
}

func toMap(v any) (map[string]json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("序列化资源: %w", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("解析资源: %w", err)
	}
	return m, nil
}

// jsonEqual 比对两段 JSON 的**语义**而不是字节。
// 字节比对会因为键序或空白差异把没变的字段报成变了。
func jsonEqual(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}
