package e2e_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
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
	// 这里原先写着「停止解析应当成功」——而那正是被修掉的那句谎：
	// 测试环境没配服务商，解析根本没变，报成功是错的。
	// dns_removed 的真伪由 TestDrainDoesNotClaimDNSRemovedWithoutSyncing 单独盯。
	//
	// 留在这里的是**每一步都必须说明自己做了什么**：一个没有 detail 的 false
	// 跟没报一样，人看不出是没做、做不了、还是做失败了。
	for _, st := range d.Steps {
		if st.Detail == "" {
			t.Errorf("%s 应当说明为什么", st.Step)
		}
	}
	// 尚未实现的两步必须报 ok=false —— 回一个 true 会让人以为流量已经排干净了。
	for _, st := range d.Steps[1:] {
		if st.OK {
			t.Errorf("%s 尚未实现，不该报成功", st.Step)
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
func TestMasterEndpointMustBeDomainNotIP(t *testing.T) {
	r := newRig(t)
	_, e := r.do("PUT", "/settings", map[string]any{"master_endpoint": "203.0.113.7:9000"})
	if e.Code != api.CodeValidation {
		t.Fatalf("code = %d，想要 %d", e.Code, api.CodeValidation)
	}
	r.mustDo("PUT", "/settings", map[string]any{"master_endpoint": "ec.internal:9000"})
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
