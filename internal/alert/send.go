package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

type channelKind int

const (
	kindWebhook channelKind = iota
	kindLark
)

type channel struct {
	name string
	url  string
	kind channelKind
}

// sendTimeout 是单次投递的上限。
//
// 取短：告警的价值在于**及时**，一条卡了 30 秒才送到的告警，人早就从别处
// 发现问题了。何况后面还有重试。
const sendTimeout = 8 * time.Second

type sender struct {
	http *http.Client
	log  *slog.Logger
	// sleep 可替换，仅供测试缩短退避。
	sleep func(time.Duration)
}

func newSender(log *slog.Logger) *sender {
	return &sender{
		http:  &http.Client{Timeout: sendTimeout},
		log:   log,
		sleep: time.Sleep,
	}
}

func (s *sender) send(ctx context.Context, c channel, ev Event, cfg Config) error {
	body, err := payload(c, ev, cfg)
	if err != nil {
		return err
	}
	attempts := cfg.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			// 线性退避。指数退避在这里没有意义：上限只有两三次，
			// 而告警拖得越久越没用。
			s.sleep(time.Duration(i) * 200 * time.Millisecond)
		}
		retryable, err := s.post(ctx, c.url, body)
		if err == nil {
			return nil
		}
		last = err
		if !retryable {
			// 4xx 是地址或凭据配错了，重试一万次也还是 4xx，
			// 只会把日志刷满而问题一点没动
			return err
		}
	}
	return fmt.Errorf("重试 %d 次后仍失败: %w", attempts-1, last)
}

// post 返回 (是否值得重试, 错误)。
func (s *sender) post(ctx context.Context, url string, body []byte) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("构造请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		// 传输层失败（连不上、超时）值得重试
		return true, fmt.Errorf("发送: %w", err)
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return false, nil
	}
	retryable := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
	return retryable, fmt.Errorf("对端返回 HTTP %d: %s", resp.StatusCode, trim(string(blob)))
}

func trim(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

func payload(c channel, ev Event, cfg Config) ([]byte, error) {
	switch c.kind {
	case kindLark:
		return larkCard(ev, cfg)
	default:
		return webhookJSON(ev)
	}
}

// webhookJSON 是通用 Webhook 的载荷：平铺的 JSON，字段名与控制台事件流一致。
func webhookJSON(ev Event) ([]byte, error) {
	return json.Marshal(map[string]any{
		"source": "edge-controller",
		"node":   ev.Node,
		"kind":   ev.Kind,
		"msg":    ev.Msg,
		"at":     ev.At.UTC().Format(time.RFC3339),
	})
}

var larkTone = map[string]string{
	"crit": "red",
	"warn": "orange",
	"ok":   "green",
}

var larkTitle = map[string]string{
	"crit": "严重告警",
	"warn": "告警",
	"ok":   "已恢复",
}

// larkCard 构造 Lark 消息卡片。
//
// 用卡片而不是纯文本：纯文本在群里就是一行灰字，严重告警和「构建成功」长得
// 一模一样。卡片有颜色和标题，扫一眼就知道要不要立刻处理。
func larkCard(ev Event, cfg Config) ([]byte, error) {
	tone, okTone := larkTone[ev.Kind]
	if !okTone {
		tone = "grey"
	}
	title := larkTitle[ev.Kind]
	if title == "" {
		title = "通知"
	}

	content := fmt.Sprintf("**节点**：%s\n**时间**：%s\n\n%s",
		ev.Node, ev.At.Local().Format("2006-01-02 15:04:05"), ev.Msg)
	// 只有严重告警才 @所有人。警告也 @ 的话，很快就没人看了。
	if cfg.AtAllOnCrit && ev.Kind == "crit" {
		content += "\n\n<at id=all></at>"
	}

	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": tone,
			"title":    map[string]any{"tag": "plain_text", "content": title + " · Edge Controller"},
		},
		"elements": []any{
			map[string]any{
				"tag":  "div",
				"text": map[string]any{"tag": "lark_md", "content": content},
			},
		},
	}
	msg := map[string]any{"msg_type": "interactive", "card": card}

	// Lark 自定义机器人的签名校验是可选的：开了才需要带 timestamp + sign。
	if cfg.LarkSecret != "" {
		ts := strconv.FormatInt(ev.At.Unix(), 10)
		msg["timestamp"] = ts
		msg["sign"] = larkSign(ts, cfg.LarkSecret)
	}
	return json.Marshal(msg)
}

// larkSign 按 Lark 的规则算签名：以 "<timestamp>\n<secret>" 为**密钥**，
// 对空消息做 HMAC-SHA256。参数顺序反了会得到一个看起来同样合理的串，
// 而对端只会回一个「签名校验失败」。
func larkSign(ts, secret string) string {
	mac := hmac.New(sha256.New, []byte(ts+"\n"+secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
