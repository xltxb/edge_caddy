package deploy_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/model"
)

// **commit 失败时，草稿必须活着，基线不能前进，且有人被告知。**
//
// 此前 commit 失败只记日志，流水线照走：基线前进、草稿被删。结果是
// 节点上跑着新配置、live 表还是旧值、草稿没了、界面说成功 ——
// 下一次任何下发把旧值推回去，而这次连草稿都找不回来（issue #31）。
// effective 的注释把这个形状叫「成功的假象里最贵的一种：它同时是数据丢失」。
func TestCommitFailureKeepsDraftsAndBaseline(t *testing.T) {
	p := newFakePusher("node-1")
	s, st := newSched(t, p)
	ctx := context.Background()

	const resKey = "route:api.example.com"
	if err := st.PutDraft(ctx, resKey,
		json.RawMessage(`{"upstream":"127.0.0.2:9090"}`), "tester"); err != nil {
		t.Fatal(err)
	}
	s.SetCommitFault(errors.New("注入：合入基线失败"))

	res, issues, err := s.Deploy(ctx, "tester", []string{resKey})
	if err != nil || len(issues) > 0 {
		t.Fatalf("下发本身不该失败（推送是成功的）：err=%v issues=%v", err, issues)
	}
	if res.OKCount != 1 {
		t.Fatalf("装置坏了：节点该收到配置，ok=%d", res.OKCount)
	}

	// 一、草稿活着 —— 它是此刻唯一还留着用户改动的地方。
	drafts, err := st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	alive := false
	for _, d := range drafts {
		if d.ResKey == resKey {
			alive = true
		}
	}
	if !alive {
		t.Error("commit 失败后草稿被删了 —— 用户的改动哪儿都不在了")
	}

	// 二、基线不前进 —— 前进等于宣称「live 渲染出来就是这一版」，而那是假的。
	// 节点比基线新会被漂移检测标出来，那个标是真话。
	baseline, err := st.Baseline(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if baseline == res.CfgVersion {
		t.Error("commit 失败后基线还是前进了 —— live 里没有这一版的内容")
	}

	// 三、有人被告知 —— 只进 Error 日志的话，没人会在出事前发现。
	events, err := st.RecentEvents(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	told := false
	for _, e := range events {
		if strings.Contains(e.Msg, "合入基线失败") && strings.Contains(e.Msg, "草稿") {
			told = true
		}
	}
	if !told {
		t.Error("commit 失败没有写事件 —— 「节点是新的、库是旧的」这件事只有日志知道")
	}
}

// **合入基线的每一步都在同一个事务里：中途失败，一处都不留。**
//
// #31 的修复只给 commit 那一步加了「失败就停」，而它后面还有五步——
// SetBaseline、三个 Bump*Versions、DeleteDrafts——每一步都只 log.Error 然后
// 继续往下走。SetBaseline 报错时基线停在旧版而草稿照样被删，随后 retry.go
// 读到 cur != job.cfgVersion，把所有掉队节点标成「已被新的下发取代」，
// 那是一句假话（issue #43）。
//
// commit 自己也是逐条 Upsert，没有事务：第 3 条失败时前 2 条已经落进 live，
// 而 deploy.go 那句注释写着「停下来之后的状态是真话：草稿还在、基线没动」。
//
// **这条测试盯的是 #31 没覆盖的那一半：live 表不能被半更新。**
// 两条路由一起下发，故障在它们之间发生——两条都不该进 live。
func TestCommitIsAtomicAcrossRoutes(t *testing.T) {
	p := newFakePusher("node-1")
	s, st := newSched(t, p)
	ctx := context.Background()

	// newSched 已经建了 api.example.com。再加一条，让这次下发有两条要写。
	if err := st.CreateRoute(ctx, model.Route{
		Domain: "web.example.com", Upstream: "127.0.0.1:8081", BlockMode: model.BlockAbort,
	}); err != nil {
		t.Fatal(err)
	}
	keys := []string{"route:api.example.com", "route:web.example.com"}
	for _, k := range keys {
		if err := st.PutDraft(ctx, k,
			json.RawMessage(`{"upstream":"127.0.0.9:9999"}`), "tester"); err != nil {
			t.Fatal(err)
		}
	}

	// 故障在事务中途：此前已经有行被写进去了。
	s.SetCommitFault(errors.New("注入：合入到一半失败"))

	if _, _, err := s.Deploy(ctx, "tester", keys); err != nil {
		t.Fatalf("下发本身不该失败（推送是成功的）：%v", err)
	}

	// live 里两条路由都该是原来的回源地址——一条都没被改。
	for _, domain := range []string{"api.example.com", "web.example.com"} {
		r, err := st.GetRoute(ctx, domain)
		if err != nil {
			t.Fatalf("读 %s: %v", domain, err)
		}
		if r.Upstream == "127.0.0.9:9999" {
			t.Errorf("%s 的 live 已经被改成草稿里的值，而这次合入是失败的——"+
				"live 被半更新了，而界面会说这次下发没成", domain)
		}
	}

	// 两条草稿都要活着：它们是此刻唯一还留着用户改动的地方。
	drafts, err := st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	alive := map[string]bool{}
	for _, d := range drafts {
		alive[d.ResKey] = true
	}
	for _, k := range keys {
		if !alive[k] {
			t.Errorf("%s 的草稿被删了——用户的改动哪儿都不在了", k)
		}
	}
}

// TestGlobalPolicyLandsInLive：全局策略的改动也要落回 live。
//
// CommitDeploy 合入的只有路由和规则。`global:` 这一类**版本被推进了、草稿被
// 删了、基线指向了新的 cfg_version**——只有 live 那一行的 spec 没动。
//
// 症状与 issue #31 一模一样，而 deploy.go 那段注释已经把它写全了：
//
//	节点上跑着新配置，而真相源里还是旧值，下一次下发会把旧值推回去。
//	而现象是「我明明改过、也下发成功了，怎么又变回去了」——中间没有任何报错。
//
// 全局策略更糟的地方在于它是全网生效的那一类：TLS 最低版本、HSTS、日志采样
// 都在里面。一次静默回退把 min_version 退回旧值，**没有任何一个页面会说这件事**
// ——工作台上没有草稿（被删了）、版本号是新的、基线是新的。
func TestGlobalPolicyLandsInLive(t *testing.T) {
	p := newFakePusher("node-1")
	s, st := newSched(t, p)
	ctx := context.Background()

	// **按字段读，不按文本匹配。** jsonb 存回来的字节与写进去的不一样
	// （键序、空格都由 PostgreSQL 决定），拿字符串比会在一个与本意无关的
	// 地方红，而那种红看起来跟真的一样。
	minVersion := func(spec json.RawMessage) string {
		var m struct {
			MinVersion string `json:"min_version"`
		}
		if err := json.Unmarshal(spec, &m); err != nil {
			t.Fatalf("策略 spec 解不开：%s", spec)
		}
		return m.MinVersion
	}

	before, err := st.GetPolicy(ctx, model.PolicyTLS)
	if err != nil {
		t.Fatal(err)
	}
	if minVersion(before.Spec) == "1.3" {
		t.Fatal("装置坏了：默认策略已经是 1.3，这次下发改不出差异")
	}

	const resKey = "global:" + model.PolicyTLS
	if err := st.PutDraft(ctx, resKey,
		// 草稿叠在整个 Policy 上，字段在 spec 里（前端的 liveByKey 存的就是
		// 整条策略）——mergeInto 对嵌套对象逐键叠加，所以 spec 的其它键不会丢。
		json.RawMessage(`{"spec":{"min_version":"1.3"}}`), "tester"); err != nil {
		t.Fatal(err)
	}

	res, issues, err := s.Deploy(ctx, "tester", []string{resKey})
	if err != nil || len(issues) > 0 {
		t.Fatalf("下发失败：err=%v issues=%v", err, issues)
	}
	if res.OKCount != 1 {
		t.Fatalf("装置坏了：节点该收到配置，ok=%d", res.OKCount)
	}

	after, err := st.GetPolicy(ctx, model.PolicyTLS)
	if err != nil {
		t.Fatal(err)
	}
	if got := minVersion(after.Spec); got != "1.3" {
		t.Fatalf("下发成功了，live 里的 min_version 还是 %q（spec=%s）—— "+
			"草稿已经删了，版本也推进了，这次改动从此哪儿都不在", got, after.Spec)
	}
}
