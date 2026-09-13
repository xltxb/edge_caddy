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

	// retryBase 是第一次重试前的等待，此后按次数递增。留空即用默认的 1 秒。
	// 做成字段只为让重试策略能被单独测——真跑 1+2 秒的测试不会有人跑
	// （与 deploy.RetryBackoff 同一条理由）。
	retryBase time.Duration
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
	results := make([]delivery, 0, 2)
	var mu sync.Mutex
	record := func(ok bool, detail string) {
		mu.Lock()
		results = append(results, delivery{ok: ok, detail: detail})
		mu.Unlock()
	}

	if cfg.WebhookURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.sendWebhook(ctx, cfg.WebhookURL, level, title, body); err != nil {
				n.Log.Error("Webhook 投递失败", "err", err)
				record(false, "webhook 失败："+err.Error())
			} else {
				record(true, "webhook 已投递")
			}
		}()
	}
	if cfg.LarkWebhook != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.sendLark(ctx, cfg.LarkWebhook, level, title, body, cfg.AtAllOnCrit); err != nil {
				n.Log.Error("Lark 投递失败", "err", err)
				record(false, "lark 失败："+err.Error())
			} else {
				record(true, "lark 已投递")
			}
		}()
	}
	wg.Wait()

	if len(results) == 0 {
		return // 一条渠道都没配，不必留痕
	}
	result := overallResult(results)
	details := make([]string, 0, len(results))
	for _, r := range results {
		details = append(details, r.detail)
	}
	detail := strings.Join(details, "；")
	if err := n.Store.InsertAudit(ctx, store.AuditRecord{
		Operator: "system", Action: "发送告警", Target: title,
		Result: result, Detail: detail,
	}); err != nil {
		n.Log.Error("写告警审计失败", "err", err)
	}
}

// delivery 是一条渠道的投递结果：**成没成是一个布尔，文案是给人读的**。
//
// 这两件事原先合在一句中文里，随后再用 `bytes.Contains(s, "失败")` 从字符串
// 搜回来（issue #77）。那句推理今天成立，只因为成功分支的文案恰好不含那两个
// 字——改一次文案它就静默失效，而失效的样子是审计里全绿。
type delivery struct {
	ok     bool
	detail string
}

// overallResult 把几条投递收成审计里的一个结果。
//
// 一条没成就是 partial 吗？不是：告警投递不是「N 个节点里 M 个成功」那种
// 部分完成——两条渠道是**同一条消息的两个出口**，有一个没送到，这次通知
// 就没有完整地发生。记 fail，让人去看 detail 里是哪一条。
func overallResult(results []delivery) string {
	for _, r := range results {
		if !r.ok {
			return "fail"
		}
	}
	return "ok"
}

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
			case <-time.After(time.Duration(i) * n.backoffBase()):
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

		// **只重试时间能修好的那些。**
		//
		// 404 / 401 / 400 说的是「地址写错了 / 凭证不对 / 载荷被拒」——
		// 再发两遍只是把同一句拒绝再听两遍，而代价是一个配错的 Webhook
		// 每条告警都打三遍（issue #56）。
		//
		// 这条判据与 ADR-0005 是同一个：「同一份字节喂给同一个 Caddy 必然
		// 得到同一个拒绝，能修它的是人改配置，不是时间」。**要挑明**：
		// 那条 ADR 的字面范围是 Caddy 配置下发，不覆盖 Webhook 投递，
		// 所以这里不是在执行它，是在复用它的理由。
		if !worthRetrying(resp.StatusCode) {
			return last
		}
	}
	return last
}

func (n *Notifier) backoffBase() time.Duration {
	if n.retryBase <= 0 {
		return time.Second
	}
	return n.retryBase
}

// worthRetrying 说明这个状态码值不值得再试一次。
//
// 5xx 是「下游此刻不行」，429 是「慢点」——两者时间都修得好。
// 其余的 4xx 要人去改配置。
func worthRetrying(status int) bool {
	return status >= 500 || status == http.StatusTooManyRequests
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
