package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/auth"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

func auditRows(t *testing.T, r *rig) []model.AuditLog {
	t.Helper()
	rows, err := r.st.ListAudit(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// 所有写操作留痕，且带上操作人与来源 IP。
func TestWritesAreAudited(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)

	_ = r.do(t, http.MethodPost, "/api/v1/routes",
		map[string]any{"domain": "audit.example.com", "upstream": "10.0.0.1:80"}, c)
	_ = r.do(t, http.MethodDelete, "/api/v1/routes/audit.example.com", nil, c)

	rows := auditRows(t, r)
	var actions []string
	for _, a := range rows {
		actions = append(actions, a.Action)
		if a.Operator == "" {
			t.Errorf("审计缺少操作人: %+v", a)
		}
		if a.SrcIP == "" {
			t.Errorf("审计缺少来源 IP（排查时要用）: %+v", a)
		}
	}
	joined := strings.Join(actions, ",")
	for _, want := range []string{"POST /routes", "DELETE /routes"} {
		if !strings.Contains(joined, want) {
			t.Errorf("应记录 %q，实际 %v", want, actions)
		}
	}
}

// 只读请求不产生审计。
//
// 把 GET 也记下来会让审计流水被巡检刷满——每 3 秒一次的节点列表轮询能在一天里
// 产生几万条，真正重要的那几条写操作就淹没了。
func TestReadsAreNotAudited(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	before := len(auditRows(t, r))

	for i := 0; i < 5; i++ {
		_ = r.do(t, http.MethodGet, "/api/v1/nodes", nil, c)
		_ = r.do(t, http.MethodGet, "/api/v1/routes", nil, c)
	}
	if after := len(auditRows(t, r)); after != before {
		t.Fatalf("只读请求不应留痕，审计从 %d 条变成 %d 条", before, after)
	}
}

// 登录**成功与失败都要留痕**。
//
// 失败登录是排查爆破的第一手线索。登录接口不经过常规鉴权中间件，
// 最容易在装配时被漏掉——这条就是为它准备的。
func TestLoginSuccessAndFailureAreAudited(t *testing.T) {
	r := newRig(t, true)

	_ = r.do(t, http.MethodPost, "/api/v1/login",
		map[string]string{"user": auth.AdminUser, "password": "wrong"}, nil)
	_ = r.login(t)

	var okCount, failCount int
	for _, a := range auditRows(t, r) {
		if !strings.Contains(a.Action, "login") {
			continue
		}
		switch a.Result {
		case "ok":
			okCount++
		case "fail":
			failCount++
		}
	}
	if failCount != 1 {
		t.Errorf("失败登录应留 1 条痕，实际 %d", failCount)
	}
	if okCount != 1 {
		t.Errorf("成功登录应留 1 条痕，实际 %d", okCount)
	}
}

// 审计里不得出现明文凭据。
func TestAuditNeverContainsCredentials(t *testing.T) {
	r := newRig(t, true)
	_ = r.do(t, http.MethodPost, "/api/v1/login",
		map[string]string{"user": auth.AdminUser, "password": testPassword}, nil)

	for _, a := range auditRows(t, r) {
		blob := a.Operator + a.Action + a.Target + a.Detail
		if strings.Contains(blob, testPassword) {
			t.Fatalf("审计里出现了明文口令: %+v", a)
		}
	}
}

// 失败的写操作也要留痕，且结果标为 fail。
//
// 只记成功的话，「有人反复尝试改某条配置但一直被拒」这件事就完全看不见。
func TestFailedWritesAreAuditedAsFailures(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	// 非法输入，必被拒
	_ = r.do(t, http.MethodPost, "/api/v1/routes",
		map[string]any{"domain": "不是域名", "upstream": "x"}, c)

	var found bool
	for _, a := range auditRows(t, r) {
		if strings.Contains(a.Action, "/routes") && a.Result == "fail" {
			found = true
		}
	}
	if !found {
		t.Fatal("失败的写操作也应留痕并标为 fail")
	}
}

// 按操作人筛选。
func TestAuditFilterByOperator(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	_ = r.do(t, http.MethodPost, "/api/v1/routes",
		map[string]any{"domain": "f.example.com", "upstream": "10.0.0.1:80"}, c)

	_, data := envelope(t, r.do(t, http.MethodGet, "/api/v1/audit?operator="+auth.AdminUser, nil, c))
	logs, _ := data["logs"].([]any)
	if len(logs) == 0 {
		t.Fatal("按本人筛选应有记录")
	}
	_, data2 := envelope(t, r.do(t, http.MethodGet, "/api/v1/audit?operator=nobody", nil, c))
	if logs2, _ := data2["logs"].([]any); len(logs2) != 0 {
		t.Fatalf("筛选不存在的操作人应为空，实际 %d 条", len(logs2))
	}
}

// ── 访问规则 ──

// 共享密钥**只写入不回显**。
//
// 接口返回它等于把凭据发给每一个能读列表的人，而列表页会被旁人看到、会进截图。
func TestRuleSecretIsNeverReturned(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	const secret = "s3cr3t-shared-value"

	rec := r.do(t, http.MethodPut, "/api/v1/rules/partner", map[string]any{
		"name": "合作方", "type": "service_secret", "enabled": true,
		"spec":     map[string]any{"header": "X-Service-Secret", "secret": secret, "ttl": 300},
		"apply_to": []string{"api.example.com"},
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("写规则应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Error("写入响应里不应回显密钥")
	}

	list := r.do(t, http.MethodGet, "/api/v1/rules", nil, c)
	if strings.Contains(list.Body.String(), secret) {
		t.Fatalf("列表里不应回显密钥: %s", list.Body.String())
	}
	// 但要能看出「已设置」，否则用户分不清是没配还是配了没显示
	if !strings.Contains(list.Body.String(), "secret_set") {
		t.Errorf("应能看出密钥是否已设置: %s", list.Body.String())
	}
}

// 未绑定域名的规则不生效，接口要如实标出来。
func TestUnboundRuleIsFlagged(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	_ = r.do(t, http.MethodPut, "/api/v1/rules/lonely", map[string]any{
		"name": "没绑域名", "type": "jwt_bearer", "enabled": true,
		"spec": map[string]any{"jwks": "https://idp.test/jwks"}, "apply_to": []string{},
	}, c)

	_, data := envelope(t, r.do(t, http.MethodGet, "/api/v1/rules", nil, c))
	rules, _ := data["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("应有 1 条规则，实际 %v", data)
	}
	if rules[0].(map[string]any)["effective"] != false {
		t.Error("未绑定域名的规则应标为不生效——那是半成品状态，不是「对所有域名生效」")
	}
}

func TestRuleInputValidation(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	for name, body := range map[string]map[string]any{
		"未知类型":       {"type": "telepathy"},
		"JWT 缺 JWKS": {"type": "jwt_bearer", "spec": map[string]any{}},
		"密钥规则缺密钥":    {"type": "service_secret", "spec": map[string]any{"header": "X-A"}},
		"白名单非法 IP":   {"type": "ip_whitelist", "spec": map[string]any{"ips": []string{"不是IP"}}},
	} {
		if rec := r.do(t, http.MethodPut, "/api/v1/rules/x", body, c); rec.Code != http.StatusBadRequest {
			t.Errorf("%s 应返回 400，实际 %d: %s", name, rec.Code, rec.Body.String())
		}
	}
}

// ── 节点操作 ──

// 对未连接的节点执行操作，必须返回**可区分**的错误。
//
// 「节点没连上」是 404 而不是 500：前者该等节点上线或去查节点，后者该查主控
// 日志或重试。混成一个的话，运维每次都得先去翻日志才能知道该往哪儿看。
func TestNodeOpsOnDisconnectedNodeReturn404(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)

	for _, path := range []string{
		"/api/v1/nodes/node-nope/probe",
		"/api/v1/nodes/node-nope/push",
		"/api/v1/nodes/node-nope/drain",
	} {
		rec := r.do(t, http.MethodPost, path, nil, c)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s 对未连接节点应返回 404，实际 %d: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "未连接") {
			t.Errorf("%s 的错误应说明是节点未连接: %s", path, rec.Body.String())
		}
	}
}

// 尚未发布过配置时，重推必须被拒绝。
//
// 此时基线是空的，重推等于把一份空配置推下去——把节点上正在跑的服务全部清掉。
// 一次「重推」变成一次全站中断。
func TestRepushBeforeAnyDeployIsRejected(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	// 让节点看起来是连着的
	r.tun.nodes = []string{"node-a"}

	rec := r.do(t, http.MethodPost, "/api/v1/nodes/node-a/push", nil, c)
	if rec.Code == http.StatusOK {
		t.Fatal("尚无基线时重推应被拒绝——那会把节点上正在跑的配置清空")
	}
	if !strings.Contains(rec.Body.String(), "尚未") {
		t.Errorf("错误应说明是还没发布过配置: %s", rec.Body.String())
	}
}

// 节点操作要留审计——它们都是有后果的写操作。
func TestNodeOpsAreAudited(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	_ = r.do(t, http.MethodPost, "/api/v1/nodes/node-nope/probe", nil, c)

	var found bool
	for _, a := range auditRows(t, r) {
		if strings.Contains(a.Action, "probe") {
			found = true
		}
	}
	if !found {
		t.Fatal("节点操作应留审计")
	}
}

// 探活返回**真实往返时延**与节点当前状态。
//
// 这条替换了原先那个只测到「消息进了发送队列」的实现：那个数字恒为零点几毫秒，
// 一台断网但 TCP 尚未超时的节点照样返回它。测的必须是对面回了话。
func TestProbeReturnsRoundTripAndNodeState(t *testing.T) {
	r := newRig(t, false)
	r.tun.nodes = []string{"node-hk-01"}
	r.tun.report = tunnel.ProbeReport{
		RTT: 42 * time.Millisecond, CfgVersion: "cfg-abc",
		CaddyOK: true, CaddyDetail: "Admin API 正常应答",
		Logs: []string{"2026-08-16T00:00:00Z INFO 配置已生效 cfg_version=cfg-abc"},
	}

	w := r.do(t, http.MethodPost, "/api/v1/nodes/node-hk-01/probe", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("探活应成功，实际 %d：%s", w.Code, w.Body.String())
	}
	data := decodeData(t, w)
	if got := data["rtt_ms"]; got != float64(42) {
		t.Errorf("往返时延应为 42ms，实际 %v", got)
	}
	if got := data["cfg_version"]; got != "cfg-abc" {
		t.Errorf("应带回节点生效版本，实际 %v", got)
	}
	if got := data["caddy_ok"]; got != true {
		t.Errorf("应带回 Caddy 可达性，实际 %v", got)
	}
	logs, _ := data["logs"].([]any)
	if len(logs) != 1 {
		t.Errorf("应带回最近日志，实际 %v", data["logs"])
	}
	// 旧实现自称只测到发送队列。真往返之后这个免责声明不该还在，
	// 留着会让人以为数字仍然不可信。
	if _, stale := data["scope"]; stale {
		t.Error("已是真实往返，不该再带 scope=master_to_queue 的免责说明")
	}
}

// Caddy 挂了不等于探活失败：隧道是通的，如实回报即可。
func TestProbeSucceedsWhenNodeCaddyIsDown(t *testing.T) {
	r := newRig(t, false)
	r.tun.nodes = []string{"node-hk-01"}
	r.tun.report = tunnel.ProbeReport{
		RTT: 5 * time.Millisecond, CaddyOK: false, CaddyDetail: "connection refused",
	}

	w := r.do(t, http.MethodPost, "/api/v1/nodes/node-hk-01/probe", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("隧道通着，探活不该失败，实际 %d：%s", w.Code, w.Body.String())
	}
	data := decodeData(t, w)
	if data["caddy_ok"] != false {
		t.Errorf("应如实回报 Caddy 不可达，实际 %v", data["caddy_ok"])
	}
	if data["caddy_detail"] != "connection refused" {
		t.Errorf("应带上不可达原因，实际 %v", data["caddy_detail"])
	}
}

// 节点连着但不回话（比如进程卡死）时，超时要说清楚是「没回报」，
// 不能报成「未连接」——后者会让人去查网络，而问题在那台机器上。
func TestProbeTimeoutIsDistinctFromNotConnected(t *testing.T) {
	r := newRig(t, false)
	r.tun.nodes = []string{"node-hk-01"}
	r.tun.probeErr = errors.New("节点 node-hk-01 探活超时（3s 内未回报）")

	w := r.do(t, http.MethodPost, "/api/v1/nodes/node-hk-01/probe", nil, nil)
	if w.Code == http.StatusNotFound {
		t.Fatal("超时不是「未连接」，不该返回 404")
	}
	if w.Code == http.StatusOK {
		t.Fatal("没收到回报就不算探活成功")
	}
	if !strings.Contains(w.Body.String(), "超时") {
		t.Errorf("错误信息应说明是超时，实际 %s", w.Body.String())
	}
}
