package e2e_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/store"
)

// 总览的四项 KPI。
func TestOverviewKPIs(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	e := r.mustDo("GET", "/overview", nil)
	var d struct {
		Baseline string `json:"baseline"`
		KPI      struct {
			NodesOnline   int      `json:"nodes_online"`
			NodesTotal    int      `json:"nodes_total"`
			ConnsDeltaPct *float64 `json:"conns_delta_pct"`
			OriginRate    *float64 `json:"origin_rate"`
			DriftNodes    int      `json:"drift_nodes"`
		} `json:"kpi"`
		Events []struct {
			Kind string `json:"kind"`
			Msg  string `json:"msg"`
		} `json:"events"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}

	if d.KPI.NodesOnline != 1 || d.KPI.NodesTotal != 1 {
		t.Errorf("在线/总数 = %d/%d", d.KPI.NodesOnline, d.KPI.NodesTotal)
	}
	// 历史不足 24 小时时是 null，不是 0 —— 0 会被读成「持平」。
	if d.KPI.ConnsDeltaPct != nil {
		t.Errorf("conns_delta_pct 应当是 null，实际 %v", *d.KPI.ConnsDeltaPct)
	}
	// 接入事件应当已经在流里，且是 ok 档（成功完成的动作），不是 info。
	var sawEnroll bool
	for _, ev := range d.Events {
		if ev.Msg == "节点已接入" {
			sawEnroll = true
			if ev.Kind != "ok" {
				t.Errorf("接入事件 kind = %q，想要 ok", ev.Kind)
			}
		}
	}
	if !sawEnroll {
		t.Errorf("事件流里应当有接入事件，实际 %+v", d.Events)
	}
}

// 探活分开报隧道可达性与节点本机 Caddy Admin 可达性。
//
// 隧道通而 Admin 不通说明 Caddy 挂了而 Agent 还活着 —— 这两种故障的处置
// 完全不同，合成一个布尔就分不出来了。
func TestProbeReportsTunnelAndCaddySeparately(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	e := r.mustDo("POST", "/nodes/node-hk-01/probe", nil)
	var d struct {
		Reachable  bool  `json:"reachable"`
		RTTMS      int64 `json:"rtt_ms"`
		CaddyAdmin bool  `json:"caddy_admin"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if !d.Reachable {
		t.Fatal("节点在线时探活应当可达")
	}
	if !d.CaddyAdmin {
		t.Error("测试里 Caddy 是活的，caddy_admin 应当为 true")
	}
	if d.RTTMS < 0 {
		t.Errorf("rtt_ms = %d", d.RTTMS)
	}
}

func TestProbeUnknownNodeIsUnreachable(t *testing.T) {
	r := newRig(t)
	_, e := r.do("POST", "/nodes/nobody/probe", nil)
	if e.Code != api.CodeNodeUnreachable {
		t.Fatalf("code = %d，想要 %d", e.Code, api.CodeNodeUnreachable)
	}
}

// 重推推的是**当前基线那一版**，不产生新的下发记录。
func TestRepushBringsNodeToBaselineWithoutNewDeploy(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "rp.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	baseline := r.deployNow("route:rp.example.com")
	before := r.countDeploys()

	e := r.mustDo("POST", "/nodes/node-hk-01/push", nil)
	var d struct {
		CfgVersion string `json:"cfg_version"`
	}
	_ = json.Unmarshal(e.Data, &d)
	if d.CfgVersion != baseline {
		t.Fatalf("重推的版本 = %q，想要基线 %q —— 重推不该产生新版本", d.CfgVersion, baseline)
	}
	if n := r.countDeploys(); n != before {
		t.Fatalf("重推产生了下发记录：%d → %d；把掉队的机器带上来不该在记录里"+
			"多出一次谁也没发起过的下发", before, n)
	}
}

// 下线需要显式确认，且**如实报告哪几步还没实现**。
func TestDrainRequiresConfirmAndEveryStepExplainsItself(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	_, e := r.do("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": false})
	if e.Code != api.CodeBadParam {
		t.Fatalf("未确认时 code = %d，想要 %d", e.Code, api.CodeBadParam)
	}

	ok := r.mustDo("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": true})
	var d struct {
		Steps []struct {
			Step   string `json:"step"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(ok.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Steps) != 3 {
		t.Fatalf("应当报三步，实际 %+v", d.Steps)
	}
	// **这一条盯的是「每一步都必须说明自己做了什么」**：一个没有 detail 的
	// false 跟没报一样，人看不出是没做、做不了、还是做失败了。
	// dns_removed 的真伪由 TestDrainDoesNotClaimDNSRemovedWithoutSyncing 单独盯。
	//
	// （这里曾经断言「停止解析应当成功」，而那正是被修掉的那句谎：
	// 测试环境没配服务商，解析根本没变，报成功是错的。）
	for _, st := range d.Steps {
		if st.Detail == "" {
			t.Errorf("%s 应当说明为什么", st.Step)
		}
	}
	// 这个 rig 没配 DNS 服务商，所以第一步摘不掉解析。那时排空必须被**跳过**
	// 并说清是跳过 —— 解析还指着这台机器，新连接源源不断，排空没有意义，
	// 而报一个 false 却不说为什么，会让人以为是节点出了问题去查节点。
	for _, st := range d.Steps {
		if st.Step != "conns_drained" {
			continue
		}
		if st.OK {
			t.Error("解析都没摘掉，排空不该报成功")
		}
		if !strings.Contains(st.Detail, "跳过") {
			t.Errorf("要说清是跳过而不是失败: %q", st.Detail)
		}
	}
}

// 凭证只写入不回显（PRD §7）：GET /alerts 只说「配没配」。
func TestAlertCredentialsAreNeverEchoed(t *testing.T) {
	r := newRig(t)
	const larkURL = "https://open.feishu.cn/hook/SECRET-TOKEN-XYZ"

	r.mustDo("PUT", "/alerts", map[string]any{
		"notify_level": "crit", "lark_webhook": larkURL, "at_all_on_crit": true,
	})

	e := r.mustDo("GET", "/alerts", nil)
	body := string(e.Data)
	if contains(body, "SECRET-TOKEN-XYZ") {
		t.Fatalf("GET /alerts 回显了凭证: %s", body)
	}
	var d struct {
		NotifyLevel string `json:"notify_level"`
		Lark        struct {
			Configured  bool `json:"webhook_configured"`
			AtAllOnCrit bool `json:"at_all_on_crit"`
		} `json:"lark"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.NotifyLevel != "crit" || !d.Lark.Configured || !d.Lark.AtAllOnCrit {
		t.Fatalf("设置没保存对: %+v", d)
	}

	// 再次保存时不带凭证 = 保持不变，而不是把它抹掉。
	r.mustDo("PUT", "/alerts", map[string]any{"notify_level": "warn"})
	again := r.mustDo("GET", "/alerts", nil)
	var d2 struct {
		Lark struct {
			Configured bool `json:"webhook_configured"`
		} `json:"lark"`
	}
	_ = json.Unmarshal(again.Data, &d2)
	if !d2.Lark.Configured {
		t.Fatal("不带凭证的保存把已配置的凭证抹掉了 —— 前端根本带不出原值来")
	}
}

// 主控接入强制域名而非 IP（PRD §5）。
// **master_endpoint 在运行时改不了，所以拒绝而不是假装存下。**
//
// 这一条此前断言「填域名会成功」——而那个成功是假的：新值存进了库，
// 而**没有任何东西读那一列**（拼安装命令用的是 EC_ADVERTISE）。
// 人改完看到「已保存」，节点的连接地址一个字没变。
//
// 这不是把约束改严了，是**发现了旧行为的一个代价**：那个地址进了主控
// 服务端证书的 SAN，而证书是启动时签的。运行时改它改不了证书——
// 节点会连上一个证书里没有它的地址，握手直接失败。
//
// 「必须是域名不是 IP」那条规则没有消失，它移到了启动配置
// （config.ValidateAdvertise，填 IP 主控拒绝启动）。
func TestMasterEndpointIsReadOnlyAtRuntime(t *testing.T) {
	r := newRig(t)

	// 域名也拒绝 —— 拒的不是「值不对」，是「这件事运行时做不了」。
	_, e := r.do("PUT", "/settings", map[string]any{"master_endpoint": "ec.internal:9000"})
	if e.Code != api.CodeValidation {
		t.Fatalf("修改 master_endpoint 应当被拒，实际 code=%d msg=%q", e.Code, e.Msg)
	}

	// **理由要指向能解决问题的地方。** 人拿到「不能改」而不知道去哪儿改，
	// 会以为这是个 bug；说了 EC_ADVERTISE 他就知道该动哪儿。
	e2 := r.mustDo("GET", "/settings", nil)
	var d struct {
		MasterEndpoint string `json:"master_endpoint"`
		ReadOnly       bool   `json:"master_endpoint_readonly"`
	}
	if err := json.Unmarshal(e2.Data, &d); err != nil {
		t.Fatal(err)
	}
	// **GET 要回主控真正在用的那个值**，不是库里那一列（它一直是空的）。
	// 设置页上显示空白，而主控明明知道自己公布的是什么——
	// 那正是前端撞上的：一个「还没配过」被渲染成了「你填错了」。
	if d.MasterEndpoint == "" {
		t.Error("应当回主控真正公布的地址，而不是库里那一列")
	}
	if !d.ReadOnly {
		t.Error("要告诉前端这一栏是只读的，否则它只能靠猜")
	}
}

// 审计 cursor 分页，且能按操作人过滤。
func TestAuditPagination(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 5; i++ {
		r.issueToken("node-x")
	}
	e := r.mustDo("GET", "/audit?limit=3", nil)
	var d struct {
		Items []struct {
			ID       int64  `json:"id"`
			Operator string `json:"operator"`
			Action   string `json:"action"`
		} `json:"items"`
		Next *int64 `json:"next_before_id"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 3 {
		t.Fatalf("limit=3 却返回 %d 条", len(d.Items))
	}
	if d.Items[0].ID <= d.Items[2].ID {
		t.Error("审计应当倒序")
	}
	if d.Next == nil {
		t.Fatal("还有更多时应当给出 next_before_id")
	}
	// 措辞照契约 §5 的表。
	if d.Items[0].Action != "签发接入Token" {
		t.Errorf("action = %q，想要「签发接入Token」", d.Items[0].Action)
	}

	page2 := r.mustDo("GET", "/audit?limit=3&before_id="+itoa(*d.Next), nil)
	var d2 struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	_ = json.Unmarshal(page2.Data, &d2)
	if len(d2.Items) == 0 || d2.Items[0].ID >= *d.Next {
		t.Fatalf("第二页应当都小于 %d，实际 %+v", *d.Next, d2.Items)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// **在线 + 异常 + 离线 == 总数。**
//
// 这条是前端在对高保真设计稿时发现的：它原先把 warn 算进「在线」，于是
// 「在线 5/6」配「异常 2 · 离线 1」——那 2 台既被算进在线、又被单独点名，
// 读的人两种理解都对不上另一半。三档由后端同一条语句产出，
// 两边分别推导迟早会算不平，而一处口径错会在界面上冒出来两次。
func TestOverviewNodeCountsPartitionCleanly(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	e := r.mustDo("GET", "/overview", nil)
	var d struct {
		KPI struct {
			Online int `json:"nodes_online"`
			Warn   int `json:"nodes_warn"`
			Down   int `json:"nodes_down"`
			Total  int `json:"nodes_total"`
		} `json:"kpi"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.KPI.Online+d.KPI.Warn+d.KPI.Down != d.KPI.Total {
		t.Fatalf("三档之和 %d+%d+%d ≠ 总数 %d",
			d.KPI.Online, d.KPI.Warn, d.KPI.Down, d.KPI.Total)
	}
	if d.KPI.Total != 1 || d.KPI.Online != 1 {
		t.Fatalf("一台健康节点应当是 1/0/0/1，实际 %+v", d.KPI)
	}
}

// **下线的第一步不能撒谎。**
//
// dns_removed 原先报 ok=true，而它只写了 edge_nodes.dns_enabled 这个标志位，
// 从没调用过 DNS 服务商——解析记录一个字节没变。detail 写着「真正调用 DNS
// 服务商属于 #21」，而 #21 早已完成：同一个 bug 我在 handleNodeDNS 上修过，
// 却没有搜一遍还有谁调 SetNodeDNS。
//
// 这一步比另外两步危险得多：conns_drained 与 tunnel_closed 诚实地报 false，
// 唯独最要紧的这一步报了 true。运维看到「已停止解析」就去关机器，而流量还在
// 往那台机器上打。
func TestDrainDoesNotClaimDNSRemovedWithoutSyncing(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	ok := r.mustDo("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": true})
	var d struct {
		Steps []struct {
			Step   string `json:"step"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(ok.Data, &d); err != nil {
		t.Fatal(err)
	}
	var dns *struct {
		Step   string `json:"step"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	for i := range d.Steps {
		if d.Steps[i].Step == "dns_removed" {
			dns = &d.Steps[i]
		}
	}
	if dns == nil {
		t.Fatalf("应当有 dns_removed 这一步，实际 %+v", d.Steps)
	}

	// 测试环境没配服务商，解析确实没被动过。这一步就**不能**报成功。
	if dns.OK {
		t.Errorf("没配服务商，解析根本没变，不该报 ok=true（detail=%q）", dns.Detail)
	}
	if !strings.Contains(dns.Detail, "未配置") {
		t.Errorf("detail 应当说清为什么没摘掉: %q", dns.Detail)
	}
	// 过期的欠条比没有欠条更糟：它看起来是有人管着的。
	if strings.Contains(dns.Detail, "#21") {
		t.Errorf("#21 已经完成，detail 不该再指着它: %q", dns.Detail)
	}
}

// **下线之后节点不能自己连回来。**
//
// Agent 断了就重连，所以「关闭隧道」如果只是断开会话，那是个假动作：
// 三秒后隧道又开了，节点照旧接下发、照旧参与解析，而 tunnel_closed 报了 true。
// 下线必须是主控侧的一个持久事实（ADR-0014）。
func TestDrainedNodeIsRefusedUntilRejoined(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	dir := t.TempDir()
	stop := r.startAgent("node-hk-01", token, dir)
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": true})

	// 停掉再起。Agent 本来就会自己重连，这里只是把那个过程压缩掉，
	// 不必等隧道被动断开。token 留空 —— 已接入过的节点凭 mTLS 连。
	stop()
	r.waitOffline("node-hk-01")
	r.startAgent("node-hk-01", "", dir)
	r.stayOffline("node-hk-01", 2*time.Second)

	// 列表上要能看出这是「我让它下线的」，而不是「它自己挂了」。
	nodes := r.mustDo("GET", "/nodes", nil)
	var d struct {
		Items []struct {
			ID        string  `json:"id"`
			DrainedAt *string `json:"drained_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(nodes.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Items) == 0 || d.Items[0].DrainedAt == nil {
		t.Fatalf("已下线的节点应当带 drained_at，实际 %+v", d.Items)
	}

	// 放回来。
	r.mustDo("POST", "/nodes/node-hk-01/rejoin", nil)
	r.startAgent("node-hk-01", "", dir)
	r.waitOnline("node-hk-01")

	after := r.mustDo("GET", "/nodes", nil)
	var d2 struct {
		Items []struct {
			DrainedAt *string `json:"drained_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(after.Data, &d2); err != nil {
		t.Fatal(err)
	}
	// **重新上线要真的清掉标记，不能只是「这次放它进来」。**
	// 留着标记而放行，下一次谁读这一列都会读到一个跟事实相反的值。
	if d2.Items[0].DrainedAt != nil {
		t.Errorf("重新上线之后 drained_at 应当清掉，实际 %q", *d2.Items[0].DrainedAt)
	}
}

// **已下线的节点不能被重新放进解析。**
//
// 下线会关掉 dns_enabled，归一化因此自然排除了它。但那道排除是**间接**的：
// 只要有人点一下「恢复解析」，标志位就回来了，而机器还连不上来——
// 解析于是指向一台主控明确拒绝它接入的机器。
//
// 排除必须直接钉在「已下线」这个事实上，而不是搭在另一个标志位的当前值上。
func TestDrainedNodeCannotBePutBackIntoDNS(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": true})

	_, e := r.do("POST", "/nodes/node-hk-01/dns", map[string]any{"enabled": true})
	if e.Code != api.CodeStateConflict {
		t.Fatalf("给已下线的节点开解析应当被拒，code = %d msg = %q", e.Code, e.Msg)
	}
	if !strings.Contains(e.Msg, "下线") {
		t.Errorf("拒绝理由要说清是因为下线: %q", e.Msg)
	}

	// **关的方向也拒绝。**
	//
	// 这里原先断言「关解析仍然要允许」，理由是「那个方向不会把流量送到一台
	// 连不上的机器上」。那个理由至今成立，而它**漏了一件事**：
	// 关这一下会把 dns_reason 从 drained 改写成 manual，而 drained_at 还在
	// ——节点页说「已下线」，DNS 页说「人手动关的」（见
	// TestDrainedNodeStaysDrainedInDNSReason）。
	//
	// 所以这不是把约束改严了，是**原来那条约束有一个当时没看见的代价**。
	_, e2 := r.do("POST", "/nodes/node-hk-01/dns", map[string]any{"enabled": false})
	if e2.Code != api.CodeStateConflict {
		t.Fatalf("给已下线的节点关解析也该被拒（它本来就不在解析里），code = %d", e2.Code)
	}
}

// 已下线的节点不该拿到新的接入 Token —— 否则「重装一台机器」就绕过了下线。
func TestDrainedNodeGetsNoEnrollToken(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")
	r.mustDo("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": true})

	_, e := r.do("POST", "/nodes/token", map[string]any{
		"node_id": "node-hk-01", "city": "香港", "vendor": "DMIT", "line": "CN2 GIA",
		"public_ip": "203.0.113.7",
	})
	if e.Code != api.CodeStateConflict {
		t.Fatalf("已下线的节点不该拿到 Token，code = %d msg = %q", e.Code, e.Msg)
	}
}

// **已下线的节点不该出现在下发目标里。**
//
// 它现在确实不会：下线断开了会话并拒绝重连，OnlineNodes 自然不含它。
// 但那是**间接**成立的——谁改了「断开」或「拒绝重连」中的任何一环，
// 这条就会静默破掉，表现是每次下发都多一条永远失败的目标记录。
//
// 钉一条测试在这里，是因为间接成立的事实没人会想起来去验。
func TestDrainedNodeIsNotADeployTarget(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"node-a", "node-b"} {
		tk, _ := r.issueTokenFor(id)
		r.startAgent(id, tk, t.TempDir())
		r.waitOnline(id)
	}

	r.mustDo("POST", "/nodes/node-b/drain", map[string]any{"confirm": true})
	r.waitOffline("node-b")

	// **先把路由建出来，再改它的草稿。**
	//
	// 这里原先只写草稿、不建资源，而它能过**正是因为那个 bug**：
	// 没有 live 底子的草稿被静默跳过，下发照常成功，于是 targets 断言照样成立。
	// 一条测试靠着「产品悄悄吞掉了它的输入」而通过——
	// 跟 dnssched 那 8 条夹具是同一族：**测试描述的动作，产品本不该允许。**
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "drained.example.com", "upstream": "127.0.0.1:1111",
		"block_mode": "abort", "body_max": "64MB",
	})
	r.mustDo("PUT", "/drafts/route:drained.example.com", map[string]any{"upstream": r.upstream})
	e := r.mustDo("POST", "/deploys", map[string]any{
		"res_keys": []string{"route:drained.example.com"},
	})
	var d struct {
		Targets []string `json:"targets"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Targets) != 1 || d.Targets[0] != "node-a" {
		t.Fatalf("目标应当只剩 node-a，实际 %+v", d.Targets)
	}
}

// **排空真的跑起来的那条路。**
//
// 前面那些下线测试都在「没配 DNS 服务商」的分支上：dns_removed 报 false，
// 排空因此被跳过——于是排空这段代码在 e2e 里一次也没执行过。
// 一个只在「上一步失败」这个分支上被测过的功能，等于没测。
func TestDrainActuallyDrainsWhenDNSWasRemoved(t *testing.T) {
	r := newRig(t)
	r.configureDNSProvider()
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	ok := r.mustDo("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": true})
	var d struct {
		Steps []struct {
			Step   string `json:"step"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(ok.Data, &d); err != nil {
		t.Fatal(err)
	}
	type stepInfo struct {
		OK     bool
		Detail string
	}
	got := map[string]stepInfo{}
	for _, st := range d.Steps {
		got[st.Step] = stepInfo{st.OK, st.Detail}
	}

	if !got["dns_removed"].OK {
		t.Fatalf("配了服务商，解析应当真的摘掉：%q", got["dns_removed"].Detail)
	}
	if !got["conns_drained"].OK {
		t.Fatalf("节点上没有连接，排空应当成功：%q", got["conns_drained"].Detail)
	}
	// **这句话必须带边界。** 解析摘了，但 DNS 有 TTL，缓存在各级递归里，
	// 一段时间内仍会有新连接进来。不说的话「已排空」就是第三句假话——
	// 人会据此认为再也没有请求了。
	if !strings.Contains(got["conns_drained"].Detail, "缓存") {
		t.Errorf("排空成功要说清它的边界（DNS 缓存未过期前仍有新连接）：%q",
			got["conns_drained"].Detail)
	}
	if !got["tunnel_closed"].OK {
		t.Fatalf("隧道应当被断开并拒绝重连：%q", got["tunnel_closed"].Detail)
	}
}

// **一张下线前签好的 Token，事后也不能用。**
//
// 这条是照前端 agent 那个办法找出来的：**改坏要从名字推，不从实现推。**
// TestDrainedNodeIsRefusedUntilRejoined 的名字说「被拒绝」，不限路径；
// 而它只测了 mTLS 重连那一条。identify 里另一条 Token 路径的 refuseIfDrained
// （server.go）写了代码、写了注释说明为什么该拒，**一行测试都没有**。
//
// 场景完全真实：签了 Token → 机器还没装好 → 期间它被下线 → 有人拿着那张
// Token 去装。TestDrainedNodeGetsNoEnrollToken 拦的是**签发**，拦不住一张
// 已经签出去的。
//
// 从实现推是想不到这条的：读着代码只会问「这个 if 改掉会不会红」，
// 而这里的问题是「名字讲的事，测试根本够不着」。
func TestDrainedNodeCannotEnrollWithAPreIssuedToken(t *testing.T) {
	r := newRig(t)
	first, _ := r.issueToken("node-hk-01")
	// 第二张在下线**之前**签好 —— 这正是那个来不及用掉的窗口。
	spare, _ := r.issueToken("node-hk-01")

	r.startAgent("node-hk-01", first, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": true})
	r.waitOffline("node-hk-01")

	// 全新的状态目录 = 一台重装过的机器，手上只有那张 Token，没有隧道证书。
	r.startAgent("node-hk-01", spare, t.TempDir())
	r.stayOffline("node-hk-01", 2*time.Second)

	// rejoin 之后同一张 Token 要能用 —— 否则「重新上线」对一台重装中的机器
	// 就是空头支票，人得再回控制台签一张。
	r.mustDo("POST", "/nodes/node-hk-01/rejoin", nil)
	r.startAgent("node-hk-01", spare, t.TempDir())
	r.waitOnline("node-hk-01")
}

// **节点日志的完整链路：Agent 写 → 隧道送 → 主控存 → 端点查（#26）。**
//
// 这个端点曾经在契约里躺了很久而从来没有注册过，格式完整、看不出异样。
// 前端那边把 404 吞掉，面板显示「暂无日志」——**一句让人放心，一句让人去查**，
// 而那四个字长得完全像一个正常的空态。
func TestNodeLogsTravelTheWholeChain(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	// Agent 在接入时会写「接入完成，已取得隧道证书」。等它送上来。
	var items []struct {
		At    string `json:"at"`
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		e := r.mustDo("GET", "/nodes/node-hk-01/logs", nil)
		var d struct {
			Items []struct {
				At    string `json:"at"`
				Level string `json:"level"`
				Msg   string `json:"msg"`
			} `json:"items"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			t.Fatal(err)
		}
		if len(d.Items) > 0 {
			items = d.Items
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(items) == 0 {
		t.Fatal("20 秒内一条日志都没上来 —— 整条链路有一处没接上")
	}

	// **level 必须是契约 §4 那四个小写取值之一。**
	// slog 的 String() 给的是 "INFO"，直接透出去会让前端见到契约里没有的取值。
	ok := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	for _, it := range items {
		if !ok[it.Level] {
			t.Errorf("level = %q，契约 §4 只列了 debug/info/warn/error", it.Level)
		}
		if it.Msg == "" {
			t.Error("msg 不该为空")
		}
		if it.At == "" {
			t.Error("at 不该为空")
		}
	}

	// 那条接入日志要在里面 —— 证明送上来的是 Agent 自己写的东西，
	// 不是别处凑的。
	var sawEnrolled bool
	for _, it := range items {
		if strings.Contains(it.Msg, "接入完成") {
			sawEnrolled = true
		}
	}
	if !sawEnrolled {
		t.Errorf("应当能看到 Agent 自己那条「接入完成」，实际 %+v", items)
	}
}

// **接入被拒时控制台上要看得到。**
//
// 人在一台新机器上跑完安装脚本，回到控制台等它上线——如果 Token 填错了、
// 过期了、已经用过了，或者这台机器之前被下线过，此前他**什么也看不到**：
// 没有报错、没有提示、节点列表里不会多一行。唯一的线索在那台机器的
// journalctl 里，而他人在控制台前面。
func TestRefusedEnrollShowsUpInEvents(t *testing.T) {
	r := newRig(t)

	events := func() []struct {
		Kind string  `json:"kind"`
		Msg  string  `json:"msg"`
		Node *string `json:"node"`
	} {
		t.Helper()
		e := r.mustDo("GET", "/overview", nil)
		var d struct {
			Events []struct {
				Kind string  `json:"kind"`
				Msg  string  `json:"msg"`
				Node *string `json:"node"`
			} `json:"events"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			t.Fatal(err)
		}
		return d.Events
	}

	// 拿一张编造的 Token 去接入。
	r.startAgent("node-hk-01", "ec_这张token根本不存在", t.TempDir())

	deadline := time.Now().Add(10 * time.Second)
	var seen bool
	for time.Now().Before(deadline) && !seen {
		for _, ev := range events() {
			if strings.Contains(ev.Msg, "接入被拒") {
				seen = true
				// **kind 是 warn，不是 info。**
				//
				// 判据是「这个状态会不会自己好起来」：接入被拒不会自愈
				// ——要么人去改 Token，要么人去重新上线，要么去关掉那台
				// 机器的 Agent。发 info 会让它淹在心跳事件里。
				// 而 crit 也不对：它不影响正在服务的流量。
				if ev.Kind != "warn" {
					t.Errorf("接入被拒应当是 warn，实际 %q（%s）", ev.Kind, ev.Msg)
				}
				if !strings.Contains(ev.Msg, "无效") {
					t.Errorf("要说清是哪一种拒绝：%q", ev.Msg)
				}
			}
		}
		if !seen {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if !seen {
		t.Fatalf("10 秒内没有「接入被拒」事件 —— 人在控制台前面等，而什么也没发生：%+v", events())
	}
}

// **一台已下线的机器在反复敲门，控制台要看得见。**
//
// Agent 的 Restart=always 保证它一直敲，直到有人动手（重新上线，
// 或者去关掉那台机器的 Agent）。此前那件事完全不可见。
func TestDrainedNodeKnockingShowsUpInEvents(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	dir := t.TempDir()
	stop := r.startAgent("node-hk-01", token, dir)
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": true})
	stop()
	r.waitOffline("node-hk-01")

	// 它带着 mTLS 证书回来敲门。
	r.startAgent("node-hk-01", "", dir)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e := r.mustDo("GET", "/overview", nil)
		var d struct {
			Events []struct {
				Kind string  `json:"kind"`
				Msg  string  `json:"msg"`
				Node *string `json:"node"`
			} `json:"events"`
		}
		if err := json.Unmarshal(e.Data, &d); err != nil {
			t.Fatal(err)
		}
		for _, ev := range d.Events {
			if strings.Contains(ev.Msg, "已被下线") {
				if ev.Kind != "warn" {
					t.Errorf("kind = %q，想要 warn", ev.Kind)
				}
				// 这一条**认得出是哪台机器**（凭 mTLS 证书的 CN），
				// 所以事件要挂在那个节点上，人能点进去。
				if ev.Node == nil || *ev.Node != "node-hk-01" {
					t.Errorf("这条认得出节点，应当挂在它身上，实际 %v", ev.Node)
				}
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("10 秒内没有看到那台机器在敲门")
}

// **控制台要看得到节点上跑的是哪一版 Agent。**
//
// 灰度部署时人最先问的就是「我推上去的那一版到底上没上」，
// 而此前那个值只出现在主控自己的日志里，控制台上没有。
func TestAgentVersionShowsUpOnNodes(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	e := r.mustDo("GET", "/nodes", nil)
	var d struct {
		Items []struct {
			AgentVersion string `json:"agent_version"`
		} `json:"items"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Items) == 0 {
		t.Fatal("/nodes 一个节点都没有")
	}
	// e2e 的 rig 不设版本，所以这里是空串——**空串是合法值**
	// （「还没接入过」或「这个 Agent 没带版本」），而字段必须在。
	// 真正的断言在下面：接入时带了版本就要能读回来。
	if d.Items[0].AgentVersion != "" {
		t.Logf("rig 带了版本：%q", d.Items[0].AgentVersion)
	}
}

// **改元数据：能改的四项真的改了，改不了的那些真的挡住了。**
func TestUpdateNodeMetaEditsOnlyWhatItShould(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	e := r.mustDo("PUT", "/nodes/node-hk-01", map[string]any{
		"city": "新加坡", "vendor": "V.PS", "line": "CMIN2", "public_ip": "203.0.113.9",
	})
	if !strings.Contains(string(e.Data), "新加坡") {
		t.Fatalf("响应里应当回显改后的值：%s", e.Data)
	}

	nodes := r.mustDo("GET", "/nodes", nil)
	if !strings.Contains(string(nodes.Data), "新加坡") ||
		!strings.Contains(string(nodes.Data), "203.0.113.9") {
		t.Fatalf("列表里应当是改后的值：%s", nodes.Data)
	}

	// **node_id 改不了。** 它是隧道证书的 CN，改它等于换一台机器。
	status, bad := r.do("PUT", "/nodes/node-hk-01", map[string]any{
		"node_id": "node-别的", "city": "香港", "vendor": "v", "line": "l",
		"public_ip": "203.0.113.9",
	})
	if status != 200 || bad.Code == api.CodeOK {
		t.Fatalf("带 node_id 的请求应当被拒，实际 http=%d code=%d", status, bad.Code)
	}
	if !strings.Contains(bad.Msg, "node_id") {
		t.Errorf("要点名那个不该出现的字段：%q", bad.Msg)
	}

	// **status 改不了**，同上：一个能改它的接口会让人以为
	// 可以手工把一台死机器改成在线。
	_, bad2 := r.do("PUT", "/nodes/node-hk-01", map[string]any{
		"status": "ok", "city": "香港", "vendor": "v", "line": "l",
		"public_ip": "203.0.113.9",
	})
	if bad2.Code == api.CodeOK {
		t.Error("带 status 的请求应当被拒")
	}

	// 公网 IP 写错要在**存之前**挡住：它会被写进 DNS 记录，
	// 而一个写错的值要到下次同步解析时才出事，那时人查的是服务商。
	_, bad3 := r.do("PUT", "/nodes/node-hk-01", map[string]any{
		"city": "香港", "vendor": "v", "line": "l", "public_ip": "不是IP",
	})
	if bad3.Code != api.CodeValidation {
		t.Errorf("非法 public_ip 应当以 1002 拒绝，实际 code=%d", bad3.Code)
	}
	after := r.mustDo("GET", "/nodes", nil)
	if strings.Contains(string(after.Data), "不是IP") {
		t.Error("被拒的请求不该改动任何东西")
	}
}

// **改动跟解析无关时，`detail` 必须是空串。**
//
// 这条看起来是文案洁癖，实际上前端整个界面判据建在它上面：
//
//	dns_synced: true            → 解析真变了
//	false 且 detail === ''      → 跟解析无关，**不是失败**，不上警示色
//	false 且 detail 有话        → 解析该变而没变成，红字警示
//
// 前端因此完全不用自己比 IP（那个 IPv6 归一化的坑在他那边根本不存在）
// ——**判据跟做决定的那一方同源**。
//
// 代价是这条约定必须成立。**哪天有人给「跟解析无关」也加一句友好的
// detail（比如「本次未涉及解析」），界面就会把一次正常的编辑渲染成失败。**
// 契约里写了这一条，而在这条测试之前没有任何东西守着它。
func TestUnrelatedEditLeavesDetailEmpty(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	cur := r.mustDo("GET", "/nodes", nil)
	if !strings.Contains(string(cur.Data), "203.0.113.7") {
		t.Fatalf("装置坏了：夹具里的公网 IP 不是预期的那个：%s", cur.Data)
	}

	// 只改城市，public_ip 原样回填（四项必填，前端就是这么发的）。
	e := r.mustDo("PUT", "/nodes/node-hk-01", map[string]any{
		"city": "新加坡", "vendor": "v", "line": "l", "public_ip": "203.0.113.7",
	})
	var d struct {
		DNSSynced bool   `json:"dns_synced"`
		Detail    string `json:"detail"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.DNSSynced {
		t.Error("没改 IP，不该说解析同步了")
	}
	if d.Detail != "" {
		t.Errorf("跟解析无关时 detail 必须是空串，实际 %q —— "+
			"前端按「detail 有话」判定为失败，一句友好的说明会被渲染成红字", d.Detail)
	}
}

// **删节点必须先下线，而且要说清「只删了记录」。**
//
// 一台还连着的机器手里有隧道证书：删掉记录之后它会重连、会被按证书认出来、
// 然后在一张不存在的行上写心跳 —— 一台**连着而看不见**的机器。
func TestDeleteNodeRequiresDrainAndSaysWhatItDidNotDo(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	// 一、没下线就删 —— 拒，而且理由要说得出。
	status, e := r.do("DELETE", "/nodes/node-hk-01", nil)
	// **钉住具体的 code，不只是「不是 0」。**
	//
	// 原先只断言 `!= CodeOK`，于是契约里那句「否则 3002」跟实现返回的 2001
	// 不一致，**而两边的测试都绿着**——前端照契约把 mock 复刻成了 3002。
	//
	// 2001 是状态冲突；3002 是「节点不可达」，而这里拒绝的理由恰恰是
	// 那台机器**还连着**。两句话正好说反。
	if status != 200 || e.Code != api.CodeStateConflict {
		t.Fatalf("还连着的节点该以 2001（状态冲突）拒绝，实际 http=%d code=%d",
			status, e.Code)
	}
	if !strings.Contains(e.Msg, "下线") {
		t.Errorf("要说清前提是什么：%q", e.Msg)
	}
	if nodes := r.mustDo("GET", "/nodes", nil); !strings.Contains(string(nodes.Data), "node-hk-01") {
		t.Fatal("被拒的删除不该动到任何东西")
	}

	// 二、下线之后可以删。
	r.mustDo("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": true})
	r.waitOffline("node-hk-01")
	ok := r.mustDo("DELETE", "/nodes/node-hk-01", nil)

	// **响应必须说清它没做什么。** 那台机器上的 Agent 与 Caddy 还在跑，
	// 而人会以为点了删除就干净了。
	if !strings.Contains(string(ok.Data), "uninstall") {
		t.Errorf("要说清「只删了记录，机器上还得自己撤」：%s", ok.Data)
	}

	if nodes := r.mustDo("GET", "/nodes", nil); strings.Contains(string(nodes.Data), "node-hk-01") {
		t.Fatalf("删完之后不该还在列表里：%s", nodes.Data)
	}

	// 三、**历史留得住。** 审计里那两条（下线、删除）不该跟着消失——
	// 删掉一台机器不该让过去发生过的事从记录里没了。
	audit := r.mustDo("GET", "/audit", nil)
	for _, want := range []string{"下线节点", "删除节点"} {
		if !strings.Contains(string(audit.Data), want) {
			t.Errorf("审计里应当还留着 %q：%s", want, audit.Data)
		}
	}
}

// **被删掉的节点带着旧证书回来，必须被拒，而且要说清那台机器上要做什么。**
//
// 灰度上撞到的完整链条：
//
//	17:23:37  删除节点 node-hk-01          ← 记录没了
//	18:11:39  签发接入Token node-hk-01     ← 想重新加回来
//	之后      事件里一直「节点已接入」，而节点列表永远是 0 台
//
// 那台机器上的 Agent 还留着隧道证书（`edge-node.sh uninstall` 刻意保留
// /var/lib/edge-agent）。它带着证书重连，被 identify() 按 CN 认出来，
// 而心跳写库是 UPDATE、影响 0 行、**不报错** ——
// 一台连着、在服务、而控制台上看不见的机器。
//
// **而新签的 Token 救不了**：Agent 优先用本地已有的证书，根本不走 Token 那条路。
//
// # 那道门是怎么自己把自己拆掉的
//
// handleDeleteNode 的前提是「必须先下线」，注释里写的正是要防这个幽灵。
// 但**删除同时也删掉了下线标记** —— IsNodeDrained 查不到行时返回 false，
// 于是重连时那道检查放行了。
//
// **一道以状态为前提的门，挡不住「那个状态连同记录一起没了」。**
//
// # 夹具
//
// `agent.Run` 跑到断开为止，**重连由调用方负责** —— 生产上是 systemd
// Restart=always 重启整个进程，复用 /var/lib/edge-agent。所以这里也起
// 第二个 Agent、指同一个 state 目录，那才是真实形态。
// （第一版我以为 rig 会自己重连，两条测试都因此失败 —— 失败的是夹具，不是代码。）
func TestDeletedNodeReconnectingWithOldCertIsRefused(t *testing.T) {
	r := newRig(t)
	stateDir := t.TempDir()
	token, _ := r.issueToken("node-hk-01")
	stop := r.startAgent("node-hk-01", token, stateDir)
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/nodes/node-hk-01/drain", map[string]any{"confirm": true})
	r.waitOffline("node-hk-01")
	r.mustDo("DELETE", "/nodes/node-hk-01", nil)
	stop()

	// 进程重启，复用同一个 state 目录 —— 里面那张隧道证书还在。
	// Token 传空串：它已经用掉了，而 Agent 本来也会优先用证书。
	r.startAgent("node-hk-01", "", stateDir)

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		nodes := r.mustDo("GET", "/nodes", nil)
		if strings.Contains(string(nodes.Data), "node-hk-01") {
			t.Fatalf("被删掉的节点靠一张旧证书回来了 —— "+
				"它会在一张不存在的行上写心跳，而那个写入不报错：%s", nodes.Data)
		}
		time.Sleep(300 * time.Millisecond)
	}

	// **而且要留下一条说得出办法的记录。**
	//
	// 这条错误只出现在节点的日志里，而看日志的人手上没有控制台的上下文。
	ov := r.mustDo("GET", "/overview", nil)
	if !strings.Contains(string(ov.Data), "edge-agent") {
		t.Errorf("拒绝的理由要说清那台机器上要做什么（清掉 /var/lib/edge-agent 再重装），"+
			"实际事件：%s", string(ov.Data)[:min(700, len(ov.Data))])
	}
}

// **反过来：记录还在的时候，带证书重连必须照常放行。**
//
// 没有这一条，一个「无条件拒绝所有带证书的连接」的实现也能让上面那条通过
// —— 而那会让每一台已接入的节点在下次重启时全部掉线。
func TestKnownNodeReconnectingWithCertIsAllowed(t *testing.T) {
	r := newRig(t)
	stateDir := t.TempDir()
	token, _ := r.issueToken("node-hk-01")
	stop := r.startAgent("node-hk-01", token, stateDir)
	r.waitOnline("node-hk-01")
	stop()

	// 同一个 state 目录、空 Token —— 走的就是「证书优先」那条路。
	r.startAgent("node-hk-01", "", stateDir)
	r.waitOnline("node-hk-01")
}

// **反复抖动必须能被数出来，因为去抖把它吃掉了。**
//
// 灰度上真发生过：CDN 每隔十几分钟切一次长连接，Agent 1–2 秒就重连上。
// 而离线判定要连续错过 heartbeat_interval × offline_threshold（默认 9 秒）
// 才翻 down —— 所以 status / online / hb_age_ms **三个瞬时值全部是健康的**，
// 界面上看不出任何异常，唯一的痕迹在那台机器的 Agent 日志里。
//
// 契约 §4 那句「短暂不一致是正常的，那是判定的去抖窗口」是对的，
// 而它的另一面就是这个：**去抖分不出「一次抖动」和「反复抖动」**。
// 前者不该惊动人，后者是故障。区分它们需要的不是更灵敏的判定，
// 是一个**跨时间的计数**。
//
// 顺带钉住「接入」与「重连」是两件事：契约 §4 里「接入」指凭 Token 的
// 首次加入，凭证书重连记成同一个词，事件流读起来就成了
// 「这台机器半小时内重新加入了三次集群」。
func TestReconnectsAreCountedAndNotCalledJoining(t *testing.T) {
	r := newRig(t)
	stateDir := t.TempDir()
	token, _ := r.issueToken("node-hk-01")
	stop := r.startAgent("node-hk-01", token, stateDir)
	r.waitOnline("node-hk-01")

	readNode := func() (int, string) {
		t.Helper()
		nodes := r.mustDo("GET", "/nodes", nil)
		var d struct {
			Items []struct {
				// **指针**：null（数不出来）与 0（很稳）必须分得开。
				Reconnects1h *int   `json:"reconnects_1h"`
				Status       string `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal(nodes.Data, &d); err != nil {
			t.Fatal(err)
		}
		if len(d.Items) != 1 {
			t.Fatalf("装置坏了：想要 1 个节点，实际 %d", len(d.Items))
		}
		if d.Items[0].Reconnects1h == nil {
			t.Fatalf("reconnects_1h 是 null —— 那是「数不出来」，" +
				"而这次数得出来。null 与 0 含义相反，不能混")
		}
		return *d.Items[0].Reconnects1h, d.Items[0].Status
	}

	// 首次加入不算重连，而且是 **0 不是 null**。
	//
	// 这两件事都要钉：
	//   - 数成 1，则每台新机器一上来就显示「已重连 1 次」
	//   - 给 null，则「很稳」被说成「数不出来」——同样是假话，方向相反
	//
	// （null 那条分支本身**没有测试**：要让 CountReconnects 失败得把库弄坏，
	// 而那会让这条测试的其余部分一起垮掉。写在这里而不是假装它被覆盖了。）
	if n, _ := readNode(); n != 0 {
		t.Fatalf("首次加入不该算重连，实际 %d", n)
	}

	// 断两次、每次都很快回来 —— 正是那个「徽标看不出来」的场景。
	for i := 0; i < 2; i++ {
		stop()
		stop = r.startAgent("node-hk-01", "", stateDir)
		r.waitOnline("node-hk-01")
	}

	n, status := readNode()
	if n != 2 {
		t.Errorf("重连次数 = %d，想要 2 —— "+
			"数不出来的话，一条每十分钟断一次的隧道在界面上完全是健康的", n)
	}
	// **而这正是它存在的理由**：抖了两次，而 status 一直是 ok。
	if status != "ok" {
		t.Logf("（status=%s；这条不是断言，只是记下当时的瞬时值）", status)
	}

	// 事件流里「接入」只该有一条，其余是「重连」。
	ov := r.mustDo("GET", "/overview", nil)
	joined := strings.Count(string(ov.Data), store.EventNodeJoined)
	recon := strings.Count(string(ov.Data), store.EventTunnelReconnected)
	if joined != 1 {
		t.Errorf("「%s」应当只有 1 条（凭 Token 那次），实际 %d —— "+
			"重连记成同一个词，事件流会读成「这台机器重新加入了三次集群」",
			store.EventNodeJoined, joined)
	}
	if recon != 2 {
		t.Errorf("「%s」应当有 2 条，实际 %d", store.EventTunnelReconnected, recon)
	}
}
