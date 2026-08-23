package config_test

import (
	"testing"

	"github.com/xltxb/edge_caddy/internal/config"
)

// TestRefusesIdenticalBotTokens 钉的是**一个配错了看不出来的安全边界**。
//
// 两个 token 设成同一个值时，比对里 ops-bot 那一支先命中，
// cert-bot 的端点白名单根本走不到 —— 人以为自己把外部平台收窄到了两个端点，
// 而实际交出去的是完整权限。
//
// **而这件事从外面看不出来**：接口照常工作，审计里记的是 ops-bot，
// 而那正是它本来就该记的东西。所以只能在启动时拒。
func TestRefusesIdenticalBotTokens(t *testing.T) {
	t.Setenv("EC_SECRET_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("EC_ADVERTISE", "cdn.example.com:9000")
	t.Setenv("EC_OPS_BOT_TOKEN", "same-token")
	t.Setenv("EC_CERT_BOT_TOKEN", "same-token")

	if _, err := config.LoadMaster(); err == nil {
		t.Fatal("两个 bot token 相同时应当拒绝启动 —— " +
			"cert-bot 的白名单不会生效，而从外面看不出来")
	}

	// 反面：不同就该放行。没有这一条，一个「有 cert token 就拒」的实现
	// 也能让上面通过。
	t.Setenv("EC_CERT_BOT_TOKEN", "different-token")
	if _, err := config.LoadMaster(); err != nil {
		t.Fatalf("两个不同的 token 不该被拒：%v", err)
	}
}
