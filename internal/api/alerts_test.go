package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// 接口**永不**回显凭据。
//
// 「回显以便确认」是最常见的凭据泄漏路径：一个只读账号、一次分享屏幕、
// 一份带响应体的接口日志，Webhook 地址就出去了——而那个地址本身就是凭据，
// 谁拿到都能往群里发消息。
func TestAlertConfigIsNeverEchoedBack(t *testing.T) {
	r := newRig(t, false)
	const hookURL = "https://hooks.example.com/services/T00/B11/XXXXSECRETXXXX"
	const larkSecret = "lark-sign-secret-9f2a"

	w := r.do(t, http.MethodPut, "/api/v1/alerts", map[string]any{
		"enabled": true, "min_level": "crit",
		"webhook_url": hookURL, "lark_secret": larkSecret,
		"lark_url": "https://open.larksuite.com/open-apis/bot/v2/hook/abcd",
	}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("保存应成功，实际 %d：%s", w.Code, w.Body.String())
	}
	// 连保存的响应里都不该有——那是最容易被忽略的一处回显
	if strings.Contains(w.Body.String(), "XXXXSECRETXXXX") {
		t.Errorf("保存响应里回显了凭据：%s", w.Body.String())
	}

	g := r.do(t, http.MethodGet, "/api/v1/alerts", nil, nil)
	if g.Code != http.StatusOK {
		t.Fatalf("读取应成功，实际 %d", g.Code)
	}
	body := g.Body.String()
	for _, plain := range []string{"XXXXSECRETXXXX", larkSecret, "hooks.example.com", "abcd"} {
		if strings.Contains(body, plain) {
			t.Errorf("读取响应里出现了凭据片段 %q：%s", plain, body)
		}
	}
	data := decodeData(t, g)
	if data["webhook_configured"] != true || data["lark_configured"] != true {
		t.Errorf("应告诉前端配过了，实际 %v", data)
	}
	if data["min_level"] != "crit" {
		t.Errorf("级别应已保存，实际 %v", data["min_level"])
	}
}

// 只改级别时凭据不该被抹掉。
//
// 前端拿不到明文，提交时那两个框必然是空的。把空当成清空的话，
// 改一次通知级别就顺手把 Webhook 抹了，而界面上一切正常——
// 直到下次真出事没人收到通知。
func TestSavingWithoutCredentialsKeepsThem(t *testing.T) {
	r := newRig(t, false)
	r.do(t, http.MethodPut, "/api/v1/alerts", map[string]any{
		"enabled": true, "min_level": "warn", "webhook_url": "https://hooks.example.com/a/b/c",
	}, nil)
	r.do(t, http.MethodPut, "/api/v1/alerts", map[string]any{
		"enabled": true, "min_level": "crit",
	}, nil)

	g := r.do(t, http.MethodGet, "/api/v1/alerts", nil, nil)
	data := decodeData(t, g)
	if data["webhook_configured"] != true {
		t.Error("只改级别不该把 Webhook 抹掉")
	}
	if data["min_level"] != "crit" {
		t.Errorf("级别应已更新，实际 %v", data["min_level"])
	}
}

// 清空要说清楚。
func TestExplicitClearRemovesWebhook(t *testing.T) {
	r := newRig(t, false)
	r.do(t, http.MethodPut, "/api/v1/alerts", map[string]any{
		"enabled": true, "webhook_url": "https://hooks.example.com/a/b/c",
	}, nil)
	r.do(t, http.MethodPut, "/api/v1/alerts", map[string]any{
		"enabled": true, "clear_webhook": true,
	}, nil)

	data := decodeData(t, r.do(t, http.MethodGet, "/api/v1/alerts", nil, nil))
	if data["webhook_configured"] != false {
		t.Error("显式清除后应显示未配置")
	}
}

// 非法级别被拒，而不是悄悄落成一个谁也不认识的值。
func TestInvalidLevelIsRejected(t *testing.T) {
	r := newRig(t, false)
	w := r.do(t, http.MethodPut, "/api/v1/alerts", map[string]any{
		"enabled": true, "min_level": "有空再说",
	}, nil)
	if w.Code == http.StatusOK {
		t.Fatal("非法通知级别应被拒绝")
	}
}

// 「发送测试」的结果写审计，成败都写。
//
// 只记成功的话，「我明明测过」与「测了但没通」在事后无法区分。
func TestSendTestIsAudited(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	r.do(t, http.MethodPut, "/api/v1/alerts", map[string]any{"enabled": true}, c)

	before := len(auditRows(t, r))
	// 一个渠道都没配，测试必然失败——失败也要留痕
	w := r.do(t, http.MethodPost, "/api/v1/alerts/test", nil, c)
	if w.Code == http.StatusOK {
		t.Fatalf("没配渠道时测试发送应失败，实际 %s", w.Body.String())
	}
	rows := auditRows(t, r)
	if len(rows) <= before {
		t.Fatal("测试发送应写审计，成败都写")
	}
	if !strings.Contains(rows[0].Action, "alerts") {
		t.Errorf("审计动作应指向告警设置，实际 %+v", rows[0])
	}
}

// 保存告警设置本身也要留痕，但**审计里不得留下凭据**。
func TestAlertConfigChangeIsAuditedWithoutSecrets(t *testing.T) {
	r := newRig(t, true)
	c := r.login(t)
	r.do(t, http.MethodPut, "/api/v1/alerts", map[string]any{
		"enabled": true, "webhook_url": "https://hooks.example.com/services/XXXXSECRETXXXX",
	}, c)

	for _, row := range auditRows(t, r) {
		if strings.Contains(row.Detail, "XXXXSECRETXXXX") {
			t.Fatalf("审计日志里留下了凭据：%+v", row)
		}
	}
}

// 未鉴权不得读写告警设置——它含凭据的「配没配」，也含能改掉告警去向的能力。
func TestAlertsRequireSession(t *testing.T) {
	r := newRig(t, true) // 设了口令，因此鉴权生效
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/alerts"},
		{http.MethodPut, "/api/v1/alerts"},
		{http.MethodPost, "/api/v1/alerts/test"},
	} {
		w := r.do(t, tc.method, tc.path, map[string]any{"enabled": true}, nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 未登录应 401，实际 %d", tc.method, tc.path, w.Code)
		}
	}
}
