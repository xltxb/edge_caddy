package alert_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/alert"
	"github.com/xltxb/edge_caddy/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

var master = []byte("test-master-key")

func sampleConfig() alert.Config {
	return alert.Config{
		Enabled: true, MinLevel: alert.LevelWarn,
		WebhookURL:  "https://hooks.example.com/services/T00/B11/XXXXSECRETXXXX",
		LarkURL:     "https://open.larksuite.com/open-apis/bot/v2/hook/abcd-1234-secret",
		LarkSecret:  "lark-sign-secret-9f2a",
		AtAllOnCrit: true, MaxRetries: 2,
	}
}

// 存进去再取出来，内容一致。
func TestConfigRoundTrips(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	want := sampleConfig()

	if err := alert.Save(ctx, st, master, want); err != nil {
		t.Fatal(err)
	}
	got, err := alert.Load(ctx, st, master)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("取回的设置与存入的不一致\n存入 %+v\n取回 %+v", want, got)
	}
}

// 凭据在库里是**密文**。
//
// 明文躺在 settings 表里是不会有任何东西提醒你的——直到有人把库文件拷走，
// 或者一次 `SELECT * FROM settings` 的截图发进了群里。
func TestCredentialsAreEncryptedAtRest(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	cfg := sampleConfig()
	if err := alert.Save(ctx, st, master, cfg); err != nil {
		t.Fatal(err)
	}

	blob, err := st.GetSetting(ctx, alert.SettingKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{
		"XXXXSECRETXXXX", "abcd-1234-secret", "lark-sign-secret-9f2a",
		"hooks.example.com", "open.larksuite.com",
	} {
		if strings.Contains(string(blob), plain) {
			t.Errorf("库里出现了明文 %q", plain)
		}
	}
}

// 主密钥不对时报错，不能悄悄当成「还没配过」返回一份空设置——
// 那会让告警在换过密钥之后静默停摆，而面板上显示「未配置」。
func TestWrongMasterKeyIsAnErrorNotAnEmptyConfig(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := alert.Save(ctx, st, master, sampleConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := alert.Load(ctx, st, []byte("wrong-key")); err == nil {
		t.Fatal("主密钥不对时必须报错")
	}
}

// 从未配置过时返回默认值，且不是错误。
func TestLoadBeforeAnyConfigReturnsDefaults(t *testing.T) {
	got, err := alert.Load(context.Background(), newStore(t), master)
	if err != nil {
		t.Fatalf("没配过不算错误: %v", err)
	}
	if got.Enabled {
		t.Error("默认应为关闭——没配渠道就打开，只会产生一串发送失败")
	}
	if got.MinLevel != alert.LevelWarn {
		t.Errorf("默认阈值应为「异常及以上」，实际 %q", got.MinLevel)
	}
}

// 对外表示里**没有**任何凭据，只有「配没配」。
//
// 「回显以便确认」是最常见的凭据泄漏路径：一个只读账号、一次分享屏幕、
// 一份带响应体的接口日志，凭据就出去了。
func TestPublicViewCarriesNoCredentials(t *testing.T) {
	cfg := sampleConfig()
	view := cfg.Public()

	blob := publicJSON(t, view)
	for _, plain := range []string{
		"XXXXSECRETXXXX", "abcd-1234-secret", "lark-sign-secret-9f2a",
		"hooks.example.com", "open.larksuite.com",
	} {
		if strings.Contains(blob, plain) {
			t.Errorf("对外表示里出现了凭据片段 %q：%s", plain, blob)
		}
	}
	if !view.WebhookConfigured || !view.LarkConfigured {
		t.Error("应告诉前端「配过了」，否则界面无法区分「没配」与「配了但看不见」")
	}
	if !view.LarkSigned {
		t.Error("应告诉前端签名密钥配过了")
	}
}

// 保存时留空表示「不改动」，而不是「清空」。
//
// 前端拿不到明文（对外表示里就没有），提交时那两个框必然是空的。
// 把空当成清空的话，改一次通知级别就会顺手把 Webhook 地址抹掉，
// 而界面上一切正常——直到下次真出事没人收到通知。
func TestEmptyCredentialOnSaveMeansUnchanged(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := alert.Save(ctx, st, master, sampleConfig()); err != nil {
		t.Fatal(err)
	}

	// 只改级别，凭据字段留空
	if err := alert.Merge(ctx, st, master, alert.Config{
		Enabled: true, MinLevel: alert.LevelCrit,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := alert.Load(ctx, st, master)
	if err != nil {
		t.Fatal(err)
	}
	if got.MinLevel != alert.LevelCrit {
		t.Errorf("级别应已更新，实际 %q", got.MinLevel)
	}
	if got.WebhookURL != sampleConfig().WebhookURL {
		t.Error("凭据留空应表示不改动，不该被抹掉")
	}
	if got.LarkSecret != sampleConfig().LarkSecret {
		t.Error("签名密钥留空应表示不改动")
	}
}

// 要清空凭据得说清楚，用显式的清除标记。
func TestExplicitClearRemovesCredential(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := alert.Save(ctx, st, master, sampleConfig()); err != nil {
		t.Fatal(err)
	}
	if err := alert.Merge(ctx, st, master, alert.Config{
		Enabled: true, MinLevel: alert.LevelWarn, ClearWebhook: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := alert.Load(ctx, st, master)
	if err != nil {
		t.Fatal(err)
	}
	if got.WebhookURL != "" {
		t.Errorf("显式清除后 Webhook 应为空，实际 %q", got.WebhookURL)
	}
	if got.LarkURL == "" {
		t.Error("只清了 Webhook，Lark 不该跟着没")
	}
}

func publicJSON(t *testing.T, v alert.PublicConfig) string {
	t.Helper()
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(blob)
}
