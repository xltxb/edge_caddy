// Package alert 把告警推给运维。
//
// 两条渠道并行发（通用 Webhook 与 Lark 群机器人），共用一个通知级别——
// 后端文档 §7 与 PRD §5 都是这么定的：级别是「什么值得打扰人」，
// 而不是「哪条渠道更重要」。
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
)

// 级别序，用来判断一条告警够不够格打扰人。
var rank = map[string]int{"ok": 0, "info": 0, "warn": 1, "crit": 2}

// levelRank 把通知级别翻成阈值。all=0 表示什么都发。
func levelRank(notifyLevel string) int {
	switch notifyLevel {
	case "all":
		return 0
	case "crit":
		return 2
	default: // warn，也是默认
		return 1
	}
}

type Notifier struct {
	Store  *store.Store
	Sealer *secret.Sealer
	Log    *slog.Logger
	HTTP   *http.Client
}

func New(st *store.Store, sealer *secret.Sealer, log *slog.Logger) *Notifier {
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{
		Store: st, Sealer: sealer, Log: log,
		HTTP: &http.Client{Timeout: 10 * time.Second},
	}
}

// Notify 按级别过滤后并行发两条渠道，结果写审计。
//
// 发送失败**不向上传播**：告警是旁路，让一次投递失败把触发它的那个操作
// 变成失败是本末倒置。但它必须留痕，否则「告警静默丢了」没人会发现。
func (n *Notifier) Notify(ctx context.Context, level, title, body string) {
	cfg, err := n.Store.GetAlertSettings(ctx, n.Sealer)
	if err != nil {
		n.Log.Error("读取告警设置失败", "err", err)
		return
	}
	if rank[level] < levelRank(cfg.NotifyLevel) {
		return
	}

	// 用不带取消的 ctx：触发告警的那个请求往往马上就结束了，
	// 跟着它取消会让告警在「刚要发出去」的时候被掐断。
	ctx = context.WithoutCancel(ctx)

	var wg sync.WaitGroup
	results := make([]string, 0, 2)
	var mu sync.Mutex
	record := func(s string) { mu.Lock(); results = append(results, s); mu.Unlock() }

	if cfg.WebhookURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.sendWebhook(ctx, cfg.WebhookURL, level, title, body); err != nil {
				n.Log.Error("Webhook 投递失败", "err", err)
				record("webhook 失败：" + err.Error())
			} else {
				record("webhook 已投递")
			}
		}()
	}
	if cfg.LarkWebhook != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.sendLark(ctx, cfg.LarkWebhook, level, title, body, cfg.AtAllOnCrit); err != nil {
				n.Log.Error("Lark 投递失败", "err", err)
				record("lark 失败：" + err.Error())
			} else {
				record("lark 已投递")
			}
		}()
	}
	wg.Wait()

	if len(results) == 0 {
		return // 一条渠道都没配，不必留痕
	}
	result := "ok"
	for _, r := range results {
		if containsFail(r) {
			result = "fail"
			break
		}
	}
	detail := strings.Join(results, "；")
	if err := n.Store.InsertAudit(ctx, store.AuditRecord{
		Operator: "system", Action: "发送告警", Target: title,
		Result: result, Detail: detail,
	}); err != nil {
		n.Log.Error("写告警审计失败", "err", err)
	}
}

func containsFail(s string) bool { return bytes.Contains([]byte(s), []byte("失败")) }

// sendWebhook 发通用 JSON，失败重试 3 次（后端文档 §7）。
func (n *Notifier) sendWebhook(ctx context.Context, url, level, title, body string) error {
	payload, err := json.Marshal(map[string]any{
		"level": level, "title": title, "body": body,
		"source": "edge-controller",
	})
	if err != nil {
		return err
	}
	return n.postWithRetry(ctx, url, payload, 3)
}

// sendLark 发 interactive 卡片。crit 且开了 at_all 时附 <at id=all></at>。
func (n *Notifier) sendLark(ctx context.Context, url, level, title, body string, atAll bool) error {
	if level == "crit" && atAll {
		body += "\n<at id=all></at>"
	}
	payload, err := json.Marshal(map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"header": map[string]any{
				"template": larkTemplate(level),
				"title":    map[string]any{"tag": "plain_text", "content": title},
			},
			"elements": []any{map[string]any{
				"tag":  "div",
				"text": map[string]any{"tag": "lark_md", "content": body},
			}},
		},
	})
	if err != nil {
		return err
	}
	return n.postWithRetry(ctx, url, payload, 3)
}

func larkTemplate(level string) string {
	switch level {
	case "crit":
		return "red"
	case "warn":
		return "orange"
	default:
		return "blue"
	}
}

func (n *Notifier) postWithRetry(ctx context.Context, url string, payload []byte, attempts int) error {
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(i) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := n.HTTP.Do(req)
		if err != nil {
			last = err
			continue
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		// 下游的原文是排查 webhook 配错的唯一线索，原样带上。
		last = fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return last
}

// Test 发一张测试卡片，供 POST /alerts/test 使用。
func (n *Notifier) Test(ctx context.Context, channel string) error {
	cfg, err := n.Store.GetAlertSettings(ctx, n.Sealer)
	if err != nil {
		return err
	}
	switch channel {
	case "lark":
		if cfg.LarkWebhook == "" {
			return fmt.Errorf("尚未配置 Lark 群机器人地址")
		}
		return n.sendLark(ctx, cfg.LarkWebhook, "info", "Edge Controller 测试卡片",
			"这是一条测试消息，收到说明 Lark 渠道配置正确。", false)
	case "webhook":
		if cfg.WebhookURL == "" {
			return fmt.Errorf("尚未配置 Webhook 地址")
		}
		return n.sendWebhook(ctx, cfg.WebhookURL, "info", "Edge Controller 测试消息",
			"这是一条测试消息，收到说明 Webhook 渠道配置正确。")
	default:
		return fmt.Errorf("未知渠道 %q", channel)
	}
}
