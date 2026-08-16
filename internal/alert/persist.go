package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
)

// SettingKey 是告警设置在 settings 表里的键。
const SettingKey = "alerts.config"

// Store 是 alert 需要的存储能力。
type Store interface {
	GetSetting(ctx context.Context, key string) ([]byte, error)
	PutSetting(ctx context.Context, key string, val []byte) error
}

// PublicConfig 是对外表示：**没有任何凭据**，只说「配没配」。
//
// 「回显以便确认」是最常见的凭据泄漏路径——一个只读账号、一次分享屏幕、
// 一份带响应体的接口日志，凭据就出去了。界面确认配没配就够了。
type PublicConfig struct {
	Enabled           bool  `json:"enabled"`
	MinLevel          Level `json:"min_level"`
	AtAllOnCrit       bool  `json:"at_all_on_crit"`
	MaxRetries        int   `json:"max_retries"`
	WebhookConfigured bool  `json:"webhook_configured"`
	LarkConfigured    bool  `json:"lark_configured"`
	LarkSigned        bool  `json:"lark_signed"`
}

// Public 生成对外表示。
func (c Config) Public() PublicConfig {
	return PublicConfig{
		Enabled: c.Enabled, MinLevel: c.MinLevel,
		AtAllOnCrit: c.AtAllOnCrit, MaxRetries: c.MaxRetries,
		WebhookConfigured: c.WebhookURL != "",
		LarkConfigured:    c.LarkURL != "",
		LarkSigned:        c.LarkSecret != "",
	}
}

// Save 把整份设置加密后落库。
func Save(ctx context.Context, st Store, master []byte, c Config) error {
	if c.MinLevel == "" {
		c.MinLevel = LevelWarn
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("序列化告警设置: %w", err)
	}
	// 整份加密而不是只加密凭据字段：只加密一部分的话，日后加一个新的凭据
	// 字段时很容易忘了把它也纳进来，而忘了是不会有任何提示的。
	sealed, err := secret.Seal(raw, master)
	if err != nil {
		return fmt.Errorf("加密告警设置: %w", err)
	}
	return st.PutSetting(ctx, SettingKey, sealed)
}

// Load 取出设置。从未配置过时返回默认值（关闭 + 异常及以上）。
func Load(ctx context.Context, st Store, master []byte) (Config, error) {
	def := Config{MinLevel: LevelWarn, MaxRetries: 2}
	blob, err := st.GetSetting(ctx, SettingKey)
	if errors.Is(err, store.ErrNotFound) || (err == nil && len(blob) == 0) {
		// 默认关闭：没配渠道就打开，只会产生一串发送失败
		return def, nil
	}
	if err != nil {
		return def, err
	}
	raw, err := secret.Open(blob, master)
	if err != nil {
		// 主密钥不对时必须报错，不能当成「还没配过」返回空设置——
		// 那会让告警在换过密钥之后静默停摆，而面板上显示「未配置」。
		return def, fmt.Errorf("解密告警设置失败（主密钥是否变了？）: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return def, fmt.Errorf("解析告警设置: %w", err)
	}
	if c.MinLevel == "" {
		c.MinLevel = LevelWarn
	}
	return c, nil
}

// Merge 用一份提交覆盖已存设置：**凭据字段留空表示不改动**。
//
// 前端拿不到明文（对外表示里就没有），提交时那两个框必然是空的。把空当成
// 清空的话，改一次通知级别就会顺手把 Webhook 地址抹掉，而界面上一切正常——
// 直到下次真出事没人收到通知。要清空得用显式的清除标记。
func Merge(ctx context.Context, st Store, master []byte, in Config) error {
	cur, err := Load(ctx, st, master)
	if err != nil {
		return err
	}
	next := cur
	next.Enabled = in.Enabled
	next.AtAllOnCrit = in.AtAllOnCrit
	next.MaxRetries = in.MaxRetries
	if in.MinLevel != "" {
		next.MinLevel = in.MinLevel
	}

	next.WebhookURL = mergeSecret(cur.WebhookURL, in.WebhookURL, in.ClearWebhook)
	next.LarkURL = mergeSecret(cur.LarkURL, in.LarkURL, in.ClearLark)
	next.LarkSecret = mergeSecret(cur.LarkSecret, in.LarkSecret, in.ClearLark || in.ClearLarkSecret)

	return Save(ctx, st, master, next)
}

func mergeSecret(cur, in string, clear bool) string {
	switch {
	case clear:
		return ""
	case in == "":
		return cur
	default:
		return in
	}
}
