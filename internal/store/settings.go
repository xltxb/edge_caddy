package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/xltxb/edge_caddy/internal/secret"
)

const (
	KeySystem = "system"
	KeyAlerts = "alerts"
)

// AlertSettings 是告警渠道配置。
//
// 两个 webhook 地址是**凭证**——它们本身就是投递权限，拿到就能往那个群里发东西。
// 因此加密落库、任何接口不回显，只回 configured 布尔（PRD §7）。
type AlertSettings struct {
	NotifyLevel string `json:"notify_level"` // all | warn | crit
	AtAllOnCrit bool   `json:"at_all_on_crit"`
	WebhookURL  string `json:"-"`
	LarkWebhook string `json:"-"`
	WebhookSet  bool   `json:"-"`
	LarkSet     bool   `json:"-"`
}

// SystemSettings 是主控接入与探活自愈的参数。
type SystemSettings struct {
	MasterEndpoint    string `json:"master_endpoint"`
	HeartbeatInterval int    `json:"heartbeat_interval_s"`
	OfflineThreshold  int    `json:"offline_threshold_count"`
	AutoDropDNS       bool   `json:"auto_drop_dns"`

	// WarnCPUPct / WarnMemPct 决定一台**连着但不健康**的机器什么时候进 warn。
	//
	// 没有这两个阈值的话 warn 永远不会被写入，而界面上「异常 N 个」那个桶
	// 就恒为 0——一个永远是零的计数比没有这个计数更糟，它会让人以为
	// 「系统看过了，没问题」。
	WarnCPUPct float64 `json:"warn_cpu_pct"`
	WarnMemPct float64 `json:"warn_mem_pct"`
}

func DefaultSystemSettings() SystemSettings {
	return SystemSettings{
		HeartbeatInterval: 3,
		OfflineThreshold:  3,
		AutoDropDNS:       true,
		WarnCPUPct:        80,
		WarnMemPct:        90,
	}
}

func DefaultAlertSettings() AlertSettings {
	return AlertSettings{NotifyLevel: "warn"}
}

// 落库时凭证以 base64 的密文放在 JSONB 里。
type alertRow struct {
	NotifyLevel string `json:"notify_level"`
	AtAllOnCrit bool   `json:"at_all_on_crit"`
	WebhookB64  string `json:"webhook_sealed,omitempty"`
	LarkB64     string `json:"lark_sealed,omitempty"`
}

func (s *Store) GetAlertSettings(ctx context.Context, sealer *secret.Sealer) (AlertSettings, error) {
	out := DefaultAlertSettings()

	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT v FROM settings WHERE k = $1`, KeyAlerts).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var row alertRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return out, fmt.Errorf("解析告警设置: %w", err)
	}

	out.NotifyLevel = row.NotifyLevel
	out.AtAllOnCrit = row.AtAllOnCrit
	out.WebhookSet = row.WebhookB64 != ""
	out.LarkSet = row.LarkB64 != ""

	// sealer 为 nil 表示调用方只要「配没配」，不要明文——GET /alerts 就是这种。
	if sealer != nil {
		if out.WebhookURL, err = openB64(sealer, row.WebhookB64); err != nil {
			return out, fmt.Errorf("解开 Webhook 地址: %w", err)
		}
		if out.LarkWebhook, err = openB64(sealer, row.LarkB64); err != nil {
			return out, fmt.Errorf("解开 Lark 地址: %w", err)
		}
	}
	return out, nil
}

// PutAlertSettings 写入。**空串表示保持不变**——凭证不回显，
// 因此前端提交时也带不出原值来（PRD §7）。
func (s *Store) PutAlertSettings(ctx context.Context, in AlertSettings, sealer *secret.Sealer) error {
	// 先读回已有的密文再覆盖：空串表示保持不变，不读回来就会把它抹掉。
	var row alertRow
	if raw, err := s.rawSettings(ctx, KeyAlerts); err != nil {
		return err
	} else if raw != nil {
		if err := json.Unmarshal(raw, &row); err != nil {
			return fmt.Errorf("解析已有告警设置: %w", err)
		}
	}

	row.NotifyLevel = in.NotifyLevel
	row.AtAllOnCrit = in.AtAllOnCrit

	// 没有密封器却要写凭证是装配漏洞。硬失败并说清楚，
	// 好过 nil 解引用 panic 之后在日志里只留下一个通用 500。
	if (in.WebhookURL != "" || in.LarkWebhook != "") && sealer == nil {
		return errors.New("要写入告警凭证，但没有可用的密封器（装配漏了 Sealer）")
	}

	if in.WebhookURL != "" {
		b, err := sealer.Seal([]byte(in.WebhookURL))
		if err != nil {
			return err
		}
		row.WebhookB64 = base64.StdEncoding.EncodeToString(b)
	}
	if in.LarkWebhook != "" {
		b, err := sealer.Seal([]byte(in.LarkWebhook))
		if err != nil {
			return err
		}
		row.LarkB64 = base64.StdEncoding.EncodeToString(b)
	}
	return s.putSettings(ctx, KeyAlerts, row)
}

func (s *Store) GetSystemSettings(ctx context.Context) (SystemSettings, error) {
	out := DefaultSystemSettings()
	raw, err := s.rawSettings(ctx, KeySystem)
	if err != nil || raw == nil {
		return out, err
	}
	return out, json.Unmarshal(raw, &out)
}

func (s *Store) PutSystemSettings(ctx context.Context, in SystemSettings) error {
	return s.putSettings(ctx, KeySystem, in)
}

func (s *Store) rawSettings(ctx context.Context, k string) ([]byte, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT v FROM settings WHERE k = $1`, k).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}

func (s *Store) putSettings(ctx context.Context, k string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx,
		`INSERT INTO settings (k, v) VALUES ($1, $2)
		 ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v`, k, b)
	return err
}

func openB64(sealer *secret.Sealer, b64 string) (string, error) {
	if b64 == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	plain, err := sealer.Open(raw)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
