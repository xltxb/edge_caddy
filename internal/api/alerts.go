package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/xltxb/edge_caddy/internal/alert"
)

// alertInput 是告警设置的提交体。
//
// 凭据字段留空表示**不改动**：前端拿不到明文（读接口里就没有），提交时那几个
// 框必然是空的。把空当成清空的话，改一次通知级别就顺手把 Webhook 抹了，
// 而界面上一切正常——直到下次真出事没人收到通知。要清空得用 clear_* 说清楚。
type alertInput struct {
	Enabled     bool   `json:"enabled"`
	MinLevel    string `json:"min_level"`
	WebhookURL  string `json:"webhook_url"`
	LarkURL     string `json:"lark_url"`
	LarkSecret  string `json:"lark_secret"`
	AtAllOnCrit bool   `json:"at_all_on_crit"`
	MaxRetries  int    `json:"max_retries"`

	ClearWebhook    bool `json:"clear_webhook"`
	ClearLark       bool `json:"clear_lark"`
	ClearLarkSecret bool `json:"clear_lark_secret"`
}

// maxRetriesCap 是重试次数的上限。
//
// 卡住是因为重试也是流量：一个 500 的 Webhook 配上 50 次重试，一次节点抖动
// 就是几十个请求，而告警本身早就过时了。
const maxRetriesCap = 5

func (h *handler) alertsReady(c *gin.Context) bool {
	if h.deps.Alerts == nil || len(h.deps.Secret) == 0 {
		fail(c, http.StatusServiceUnavailable, codeInternal, "告警功能未装配")
		return false
	}
	return true
}

// getAlerts 返回**不含任何凭据**的对外表示。
func (h *handler) getAlerts(c *gin.Context) {
	if !h.alertsReady(c) {
		return
	}
	cfg, err := alert.Load(c.Request.Context(), h.deps.Store, h.deps.Secret)
	if err != nil {
		h.failErr(c, err, "读取告警设置失败")
		return
	}
	st := h.deps.Alerts.Stats()
	ok(c, gin.H{
		"enabled": cfg.Public().Enabled, "min_level": cfg.Public().MinLevel,
		"at_all_on_crit":     cfg.Public().AtAllOnCrit,
		"max_retries":        cfg.Public().MaxRetries,
		"webhook_configured": cfg.Public().WebhookConfigured,
		"lark_configured":    cfg.Public().LarkConfigured,
		"lark_signed":        cfg.Public().LarkSigned,
		// 投递计数：失败必须能被发现。静默失败的告警系统比没有告警更糟——
		// 人以为自己被保护着。
		"sent": st.Sent, "failed": st.Failed, "dropped": st.Dropped,
	})
}

// putAlerts 保存设置。响应里同样不回显凭据。
func (h *handler) putAlerts(c *gin.Context) {
	if !h.alertsReady(c) {
		return
	}
	var in alertInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, codeBadInput, "请求体不是合法 JSON")
		return
	}
	level := alert.Level(in.MinLevel)
	switch level {
	case "", alert.LevelAll, alert.LevelWarn, alert.LevelCrit:
	default:
		// 悄悄落一个谁也不认识的值，等于把过滤退化成「全发」或「全不发」，
		// 而界面上显示的是用户填的那个词
		fail(c, http.StatusUnprocessableEntity, codeBadInput,
			"通知级别只能是 all / warn / crit，收到 "+in.MinLevel)
		return
	}
	if in.MaxRetries < 0 || in.MaxRetries > maxRetriesCap {
		fail(c, http.StatusUnprocessableEntity, codeBadInput,
			"重试次数须在 0 到 5 之间——重试也是流量，而告警拖久了就没用了")
		return
	}

	next := alert.Config{
		Enabled: in.Enabled, MinLevel: level,
		WebhookURL: in.WebhookURL, LarkURL: in.LarkURL, LarkSecret: in.LarkSecret,
		AtAllOnCrit: in.AtAllOnCrit, MaxRetries: in.MaxRetries,
		ClearWebhook: in.ClearWebhook, ClearLark: in.ClearLark, ClearLarkSecret: in.ClearLarkSecret,
	}
	if err := alert.Merge(c.Request.Context(), h.deps.Store, h.deps.Secret, next); err != nil {
		h.failErr(c, err, "保存告警设置失败")
		return
	}
	saved, err := alert.Load(c.Request.Context(), h.deps.Store, h.deps.Secret)
	if err != nil {
		h.failErr(c, err, "读取告警设置失败")
		return
	}
	// 落库成功才让运行中的通知器换配置：反过来的话，一次写库失败会留下
	// 「内存里已生效、重启后回退」的不一致。
	h.deps.Alerts.SetConfig(saved)
	h.log.Info("告警设置已更新", "operator", operatorOf(c),
		"enabled", saved.Enabled, "min_level", saved.MinLevel)
	// 回的是对外表示，连保存响应里也没有凭据——那是最容易被忽略的一处回显
	ok(c, saved.Public())
}

// testAlerts 发一张测试卡片，同步等结果。
func (h *handler) testAlerts(c *gin.Context) {
	if !h.alertsReady(c) {
		return
	}
	cfg, err := alert.Load(c.Request.Context(), h.deps.Store, h.deps.Secret)
	if err != nil {
		h.failErr(c, err, "读取告警设置失败")
		return
	}
	h.deps.Alerts.SetConfig(cfg)

	if err := h.deps.Alerts.SendTest(c.Request.Context()); err != nil {
		// 成败都写审计（中间件按状态码判定）：只记成功的话，
		// 「我明明测过」与「测了但没通」在事后无法区分。
		h.log.Warn("告警测试发送失败", "operator", operatorOf(c), "err", err)
		fail(c, http.StatusBadGateway, codeInternal, "测试发送失败："+err.Error())
		return
	}
	h.log.Info("告警测试已发送", "operator", operatorOf(c))
	ok(c, gin.H{"sent": true})
}
