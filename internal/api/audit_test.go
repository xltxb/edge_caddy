package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/auth"
	"github.com/xltxb/edge_caddy/internal/model"
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
