package e2e_test

import (
	"context"
	"encoding/json"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
	"strings"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/api"
)

type weightsResp struct {
	Domain string `json:"domain"`
	Lines  []struct {
		Code    string `json:"code"`
		Name    string `json:"name"`
		Entries []struct {
			Node   string  `json:"node"`
			Weight int     `json:"weight"`
			Share  float64 `json:"share"`
		} `json:"entries"`
	} `json:"lines"`
	Capabilities struct {
		Kind    string   `json:"kind"`
		Lines   []string `json:"lines"`
		Weights bool     `json:"weights"`
		Notes   string   `json:"notes"`
	} `json:"capabilities"`
}

func (r *rig) weights() weightsResp {
	r.t.Helper()
	e := r.mustDo("GET", "/dns/weights", nil)
	var w weightsResp
	if err := json.Unmarshal(e.Data, &w); err != nil {
		r.t.Fatal(err)
	}
	return w
}

// 五条线路始终齐全，即使一条都没配 —— 前端按线路分组渲染，
// 缺一条会让那一组凭空消失，而不是显示成「这条线还没配」。
func TestDNSWeightsAlwaysReturnsFiveLines(t *testing.T) {
	r := newRig(t)
	w := r.weights()
	if len(w.Lines) != 5 {
		t.Fatalf("线路数 = %d，想要 5", len(w.Lines))
	}
	for i, code := range []string{"ct", "cu", "cm", "tw", "ov"} {
		if w.Lines[i].Code != code {
			t.Errorf("第 %d 条 = %s，想要 %s", i, w.Lines[i].Code, code)
		}
	}
}

// **一台刚接入的节点，必须在这一页上出现得了。**
//
// 原先它不出现：Build 只遍历 dns_weights 里的行，而写那张表的唯一入口是
// PUT /dns/weights——界面上只能改「已经在列表里的节点」。**闭环**，
// 新接入的节点永远进不了解析，而页面看起来完全正常：五条线路齐全，只是全空。
//
// 单元测试盯的是 Build 的候选逻辑；这条盯的是**整条路真的走得通**：
// 接入 → 出现在五条线路上（权重 0）→ 给它配上权重 → 真的进了轮换。
// 少了最后一步，这条测试就只证明了「它显示出来了」，而人要的是「它能用」。
func TestFreshNodeCanBeGivenWeight(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	// 一、一行权重都没配过，它就该在五条线路上都出现。
	w := r.weights()
	for _, l := range w.Lines {
		var found bool
		for _, e := range l.Entries {
			if e.Node == "node-hk-01" {
				found = true
				if e.Weight != 0 || e.Share != 0 {
					t.Errorf("%s：还没配过的节点应当是 weight=0 share=0，实际 %+v", l.Code, e)
				}
			}
		}
		if !found {
			t.Fatalf("线路 %s 上没有刚接入的节点 —— 那它就永远配不上权重", l.Code)
		}
	}

	// 二、给它配上权重，它要真的进轮换。
	r.mustDo("PUT", "/dns/weights", map[string]any{
		"lines": []any{map[string]any{
			"code":    "cu",
			"entries": []any{map[string]any{"node": "node-hk-01", "weight": 50}},
		}},
	})
	w = r.weights()
	for _, l := range w.Lines {
		if l.Code != "cu" {
			continue
		}
		for _, e := range l.Entries {
			if e.Node == "node-hk-01" {
				if e.Weight != 50 || e.Share != 100 {
					t.Fatalf("配上权重之后应当独占联通线路，实际 %+v", e)
				}
				return
			}
		}
	}
	t.Fatal("配完权重之后反而找不到它了")
}

// **没配服务商时如实说清楚**：权重只会保存在本地，不会推到任何地方。
//
// 不说的话，人配了一堆权重、界面显示保存成功，而解析根本没动过。
func TestDNSCapabilitiesSayWhenNoProviderConfigured(t *testing.T) {
	r := newRig(t)
	w := r.weights()
	if w.Capabilities.Kind != "" {
		t.Errorf("还没配服务商，kind 应当为空，实际 %q", w.Capabilities.Kind)
	}
	if !strings.Contains(w.Capabilities.Notes, "尚未配置") {
		t.Errorf("说明里应当讲清还没配服务商: %q", w.Capabilities.Notes)
	}
}

// 没配服务商时权重仍然可以保存 —— 那是本地的意图，
// 没有服务商不代表不能先配好。
func TestDNSWeightsSaveLocallyWithoutProvider(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("PUT", "/dns/weights", map[string]any{
		"lines": []any{map[string]any{
			"code":    "ct",
			"entries": []any{map[string]any{"node": "node-hk-01", "weight": 100}},
		}},
	})

	w := r.weights()
	for _, l := range w.Lines {
		if l.Code != "ct" {
			continue
		}
		if len(l.Entries) != 1 || l.Entries[0].Weight != 100 || l.Entries[0].Share != 100 {
			t.Fatalf("电信线路 = %+v，想要 weight=100 share=100", l.Entries)
		}
		return
	}
	t.Fatal("没找到电信线路")
}

func TestDNSWeightsRejectsUnknownLineAndNegativeWeight(t *testing.T) {
	r := newRig(t)

	_, e := r.do("PUT", "/dns/weights", map[string]any{
		"lines": []any{map[string]any{"code": "火星", "entries": []any{}}},
	})
	if e.Code != api.CodeValidation {
		t.Fatalf("未知线路码 code = %d，想要 %d", e.Code, api.CodeValidation)
	}

	_, e2 := r.do("PUT", "/dns/weights", map[string]any{
		"lines": []any{map[string]any{
			"code":    "ct",
			"entries": []any{map[string]any{"node": "a", "weight": -1}},
		}},
	})
	if e2.Code != api.CodeValidation {
		t.Fatalf("负权重 code = %d，想要 %d", e2.Code, api.CodeValidation)
	}
	var d struct {
		Errors []struct {
			Field string `json:"field"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(e2.Data, &d)
	if len(d.Errors) == 0 || !strings.Contains(d.Errors[0].Field, "weight") {
		t.Fatalf("错误应当定位到具体的权重字段，实际 %+v", d.Errors)
	}
}

// 被摘除的节点占比归零，其余节点重新归一化 —— 端到端验一遍。
func TestDNSSharesRenormalizeWhenNodePaused(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"node-a", "node-b"} {
		token, _ := r.issueTokenFor(id)
		r.startAgent(id, token, t.TempDir())
		r.waitOnline(id)
	}

	r.mustDo("PUT", "/dns/weights", map[string]any{
		"lines": []any{map[string]any{
			"code": "ct",
			"entries": []any{
				map[string]any{"node": "node-a", "weight": 60},
				map[string]any{"node": "node-b", "weight": 40},
			},
		}},
	})

	// 暂停 b 的解析。
	r.mustDo("POST", "/nodes/node-b/dns", map[string]any{"enabled": false})

	w := r.weights()
	for _, l := range w.Lines {
		if l.Code != "ct" {
			continue
		}
		got := map[string]float64{}
		weights := map[string]int{}
		for _, e := range l.Entries {
			got[e.Node] = e.Share
			weights[e.Node] = e.Weight
		}
		if got["node-a"] != 100 || got["node-b"] != 0 {
			t.Fatalf("占比 = %+v，想要 a=100 b=0", got)
		}
		// Weight 是配置值，不该被自愈改掉 —— 人没动过它。
		if weights["node-b"] != 40 {
			t.Errorf("被暂停的节点权重应当保持 40，实际 %d", weights["node-b"])
		}
		return
	}
	t.Fatal("没找到电信线路")
}

// DNS 服务商凭证只写入不回显（PRD §7）。
func TestDNSProviderCredentialNotEchoed(t *testing.T) {
	r := newRig(t)
	const cred = "dnspod-SECRET-TOKEN-42"

	r.mustDo("PUT", "/settings", map[string]any{
		"dns_provider": map[string]any{
			"kind": "dnspod", "domain": "example.com", "sub": "cdn", "credential": cred,
		},
	})

	e := r.mustDo("GET", "/settings", nil)
	if strings.Contains(string(e.Data), "SECRET-TOKEN") {
		t.Fatalf("GET /settings 回显了 DNS 凭证: %s", e.Data)
	}
	var d struct {
		DNSProvider struct {
			Kind       string `json:"kind"`
			Domain     string `json:"domain"`
			Configured bool   `json:"configured"`
		} `json:"dns_provider"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.DNSProvider.Kind != "dnspod" || d.DNSProvider.Domain != "example.com" || !d.DNSProvider.Configured {
		t.Fatalf("设置没保存对: %+v", d.DNSProvider)
	}

	// 不带凭证再保存一次 = 保持不变，不是抹掉。
	r.mustDo("PUT", "/settings", map[string]any{
		"dns_provider": map[string]any{"sub": "www"},
	})
	again := r.mustDo("GET", "/settings", nil)
	var d2 struct {
		DNSProvider struct {
			Sub        string `json:"sub"`
			Configured bool   `json:"configured"`
		} `json:"dns_provider"`
	}
	_ = json.Unmarshal(again.Data, &d2)
	if !d2.DNSProvider.Configured {
		t.Fatal("不带凭证的保存把已配置的凭证抹掉了")
	}
	if d2.DNSProvider.Sub != "www" {
		t.Errorf("其余字段应当被更新，sub = %q", d2.DNSProvider.Sub)
	}
}

// **手动切换解析要真的同步到服务商，并如实说明同步了没有。**
//
// 这里原先只改标志位，注释写着「真正调服务商属于 #21」——而 #21 完成之后
// 那句话没跟着改。于是心跳超时的**自动**摘除会同步服务商，人手动点
// 「暂停解析」却不会：同一件事两条路径行为不一致，而不一致的那条恰恰是
// 人主动做的那条。
func TestNodeDNSToggleReportsWhetherProviderWasSynced(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	e := r.mustDo("POST", "/nodes/node-hk-01/dns", map[string]any{"enabled": false})
	var d struct {
		DNSEnabled bool   `json:"dns_enabled"`
		DNSSynced  bool   `json:"dns_synced"`
		Detail     string `json:"detail"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.DNSEnabled {
		t.Fatal("标志位应当被关掉")
	}
	// 测试环境没配服务商，因此**必须说出来**，而不是让人以为解析已经变了。
	if d.DNSSynced {
		t.Error("没配服务商时不该声称已同步")
	}
	if !strings.Contains(d.Detail, "未配置") {
		t.Errorf("detail 应当说清解析没被动过: %q", d.Detail)
	}
}

// **常驻的同步状态。**
//
// POST /nodes/:id/dns 的 dns_synced 只出现一次就消失了，而界面上
// 「已退出解析」那类徽标是常驻的。没有一个常驻的真相来源，一次失败的同步
// 会留下一个一直撒谎到下次有人再点开关为止的徽标。
func TestDNSSyncStateIsPersistedAndExposed(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	// 还没同步过：ok=false 是对的 —— 服务商那边确实没反映过我们的意图。
	nodes := r.mustDo("GET", "/nodes", nil)
	var n struct {
		DNSSync struct {
			OK     bool    `json:"ok"`
			At     *string `json:"at"`
			Detail string  `json:"detail"`
		} `json:"dns_sync"`
	}
	if err := json.Unmarshal(nodes.Data, &n); err != nil {
		t.Fatal(err)
	}
	if n.DNSSync.OK {
		t.Error("从来没同步过时不该说 ok")
	}
	if !strings.Contains(n.DNSSync.Detail, "尚未") {
		t.Errorf("detail 应当说清从没同步过: %q", n.DNSSync.Detail)
	}
	// **从来没同步过时 at 必须是 null，不是零值时间。**
	// 0001-01-01T00:00:00Z 会被渲染成 00:00:00，读起来像「凌晨同步过一次」
	// ——一个格式正确但意思是假的值，比缺失的值危险：空白会让人去查，
	// 一个像模像样的时间不会。
	if n.DNSSync.At != nil {
		t.Fatalf("从没同步过时 at 应当是 null，实际 %q", *n.DNSSync.At)
	}

	// 点一次开关（没配服务商，同步会失败），状态要被记下来。
	r.mustDo("POST", "/nodes/node-hk-01/dns", map[string]any{"enabled": false})

	after := r.mustDo("GET", "/dns/weights", nil)
	var w struct {
		DNSSync struct {
			OK     bool   `json:"ok"`
			At     string `json:"at"`
			Detail string `json:"detail"`
		} `json:"dns_sync"`
	}
	if err := json.Unmarshal(after.Data, &w); err != nil {
		t.Fatal(err)
	}
	if w.DNSSync.OK {
		t.Fatal("没配服务商时同步不该报成功")
	}
	if w.DNSSync.At == "" {
		t.Error("应当记下尝试的时间")
	}
	if !strings.Contains(w.DNSSync.Detail, "服务商") {
		t.Errorf("失败原因应当留下来: %q", w.DNSSync.Detail)
	}
}

// **「未参与解析」要说得出为什么、是谁、什么时候。**
//
// 三条路径关掉解析，而它们的处置完全不同：人自己关的想开就开，
// 系统自动摘的要先去修那台机器，人下线的要先「重新上线」。
// 只有一个 dns_enabled 布尔的时候，三者在数据里长得一模一样。
func TestDNSChangeCarriesReasonActorAndTime(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	read := func() (reason string, actor *string, at *string) {
		t.Helper()
		e := r.mustDo("GET", "/nodes", nil)
		var d struct {
			Items []struct {
				DNSReason    string  `json:"dns_reason"`
				DNSActor     *string `json:"dns_actor"`
				DNSChangedAt *string `json:"dns_changed_at"`
			} `json:"items"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			t.Fatal(err)
		}
		if len(d.Items) == 0 {
			t.Fatal("/nodes 一个节点都没有")
		}
		return d.Items[0].DNSReason, d.Items[0].DNSActor, d.Items[0].DNSChangedAt
	}

	// 还没人动过：三样都空 —— 空与「manual 且操作人不详」是两回事。
	if reason, actor, at := read(); reason != "" || actor != nil || at != nil {
		t.Fatalf("没人动过时三样都该是空的：reason=%q actor=%v at=%v", reason, actor, at)
	}

	// 人手动关。
	r.mustDo("POST", "/nodes/node-hk-01/dns", map[string]any{"enabled": false})
	reason, actor, at := read()
	if reason != "manual" {
		t.Errorf("人手动关，reason 应当是 manual，实际 %q", reason)
	}
	if actor == nil || *actor != "abiu" {
		t.Errorf("应当记下操作人，实际 %v", actor)
	}
	if at == nil {
		t.Fatal("应当记下时间")
	}
	// 时间要是真时刻，不是零值 —— 一个 0001-01-01 会被渲染成
	// 「凌晨关的」，而那是个格式正确、意思是假的值（契约 §0.4）。
	ts, err := time.Parse(time.RFC3339, *at)
	if err != nil {
		t.Fatalf("时间解析不了：%q", *at)
	}
	if time.Since(ts) > time.Minute || ts.Year() < 2020 {
		t.Errorf("应当是刚刚那一刻，实际 %q", *at)
	}

	// 人下线：原因要变成 drained，而不是留着上一次的 manual。
	r.mustDo("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": true})
	if reason, actor, _ := read(); reason != "drained" {
		t.Errorf("下线之后 reason 应当是 drained，实际 %q（actor=%v）", reason, actor)
	}
}

// 系统自动摘除时**操作人是 null，不是「system」**。
//
// 一个叫 system 的操作人会在界面上冒出一个不存在的账号，而人会去问那是谁。
func TestAutoDetachHasNoActor(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	if err := st.UpsertNode(ctx, store.NodeSpec{
		NodeID: "node-a", City: "香港", Vendor: "DMIT", Line: "CN2 GIA",
		PublicIP: "203.0.113.7",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeDown(ctx, "node-a"); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := nodes[0]
	if n.DNSReason != store.DNSAutoOffline {
		t.Errorf("reason = %q，想要 %q", n.DNSReason, store.DNSAutoOffline)
	}
	if n.DNSActor != "" {
		t.Errorf("系统自动摘除不该有操作人，实际 %q", n.DNSActor)
	}
	if n.DNSChangedAt == nil {
		t.Error("应当记下时间")
	}
	if n.DNSEnabled {
		t.Error("自动摘除应当同时关掉解析")
	}
}

// **一台已下线的机器，不该出现「已下线」而 dns_reason 说「人手动关的」。**
//
// 前端 agent 的 seed 里出现过这个矛盾（节点页说「已下线（人为）」，
// DNS 页说「已暂停（abiu）」），他那是夹具推错了。这条查的是**后端有没有
// 一条真实路径能产生同一个状态**——关解析此前不挡已下线的节点，
// 而那一下会把 dns_reason 从 drained 改写成 manual。
//
// **两句单独看都对，只有并排才看得出对不上账。**
func TestDrainedNodeStaysDrainedInDNSReason(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": true})

	// 对一台已下线的机器点「暂停解析」——它已经不在解析里了。
	_, e := r.do("POST", "/nodes/node-hk-01/dns", map[string]any{"enabled": false})

	nodes := r.mustDo("GET", "/nodes", nil)
	var d struct {
		Items []struct {
			DrainedAt *string `json:"drained_at"`
			DNSReason string  `json:"dns_reason"`
			DNSActor  *string `json:"dns_actor"`
		} `json:"items"`
	}
	if err := json.Unmarshal(nodes.Data, &d); err != nil {
		t.Fatal(err)
	}
	n := d.Items[0]
	if n.DrainedAt == nil {
		t.Fatal("前置条件不成立：它应当是已下线的")
	}
	if n.DNSReason != "drained" {
		t.Errorf("已下线的机器 dns_reason 应当是 drained，实际 %q —— "+
			"节点页会说「已下线」而 DNS 页说「人手动关的」，两句对不上账 "+
			"(dns 请求 code=%d msg=%q)", n.DNSReason, e.Code, e.Msg)
	}
}

// **系统自动摘除时 `dns_actor` 在 JSON 里必须是 `null`，不是空串。**
//
// 这条是前端 agent 点出来的一处「什么情况下返回空」的约定：
// 契约 §4 写着 auto_offline 时 `dns_actor` 是 `null`，
// 而**在这条测试之前，只有 store 那一层被验过**——
// `store.Node.DNSActor` 是 `string`（SQL 里 coalesce 成了空串），
// 所以那条测试断言的是 `== ""`。把 `nodeResp.DNSActor` 从 `*string`
// 改成 `string`，它照样全绿，而 JSON 里会变成 `""`。
//
// 空串与 null 在这里不是风格问题（契约 §0.4）：
// 界面按「有没有操作人」分岔——有就说「谁关的」，没有就说「系统自动摘的，
// **先去修那台机器**」。空串会走进第一条分支，然后显示一个空的名字。
//
// 顺带钉住反面：人手动关的时候必须**有**操作人。
// 少了这一条，一个「永远返回 null」的实现也能让上半条通过。
func TestAutoOfflineHasNullActorInJSON(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	read := func() (string, *string) {
		t.Helper()
		nodes := r.mustDo("GET", "/nodes", nil)
		var d struct {
			Items []struct {
				DNSReason string  `json:"dns_reason"`
				DNSActor  *string `json:"dns_actor"`
			} `json:"items"`
		}
		if err := json.Unmarshal(nodes.Data, &d); err != nil {
			t.Fatal(err)
		}
		if len(d.Items) != 1 {
			t.Fatalf("装置坏了：想要 1 个节点，实际 %d", len(d.Items))
		}
		return d.Items[0].DNSReason, d.Items[0].DNSActor
	}

	// 一、人手动关：必须有操作人。
	r.mustDo("POST", "/nodes/node-hk-01/dns", map[string]any{"enabled": false})
	if reason, actor := read(); reason != "manual" || actor == nil || *actor == "" {
		t.Fatalf("人手动关的要记下是谁，实际 reason=%q actor=%v", reason, actor)
	}

	// 二、**解析本来就是关着的时候，自动摘除不覆盖 reason。**
	//
	// 这一段此前写的是「手动关掉之后直接 SetNodeDown，期望 reason 变成
	// auto_offline」—— 而那正是一个 bug 的编码：一台**人手动关了解析**的机器
	// 掉线之后，人的决定被覆盖成「系统摘的」，而心跳一恢复系统又会把解析
	// 开回去（ReattachAfterRecovery 只放回 auto_offline 那一种）。
	// **一次掉线撤销了一个人为的决定，而没有任何地方记下这件事。**
	//
	// 修完之后这条测试红了。**红的原因是它原先在验一件错的事。**
	if err := r.store.SetNodeDown(context.Background(), "node-hk-01"); err != nil {
		t.Fatal(err)
	}
	if reason, actor := read(); reason != "manual" || actor == nil {
		t.Fatalf("解析本来就是人关着的，自动摘除不该覆盖它，"+
			"实际 reason=%q actor=%v", reason, actor)
	}

	// 三、**解析开着的时候被自动摘除：reason 是 auto_offline、actor 是 null。**
	//
	// 空串会让界面走进「谁关的」那条分支，然后显示一个空的名字。
	r.mustDo("POST", "/nodes/node-hk-01/dns", map[string]any{"enabled": true})
	if err := r.store.SetNodeDown(context.Background(), "node-hk-01"); err != nil {
		t.Fatal(err)
	}
	reason, actor := read()
	if reason != "auto_offline" {
		t.Fatalf("解析开着时被自动摘除，reason 该是 auto_offline，实际 %q", reason)
	}
	if actor != nil {
		t.Errorf("系统自动摘除时 dns_actor 必须是 null，实际 %q", *actor)
	}
}
