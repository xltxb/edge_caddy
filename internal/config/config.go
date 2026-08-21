// Package config 收拢主控与 Agent 的启动配置。
//
// 全部来自环境变量，没有配置文件：这套东西由 systemd 拉起，
// 而 systemd 本来就有 Environment= 与 EnvironmentFile=。再加一层
// 配置文件只会多出「文件里写的和 unit 里写的哪个生效」这个问题。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Master struct {
	DatabaseURL string
	HTTPAddr    string
	GRPCAddr    string
	SecretKey   []byte

	// MTLS 默认关。见 docs/adr/0013-console-access-is-network-plus-session.md：
	// 控制台准入首版靠「只绑内网 + 会话 Cookie + 全写审计」，mTLS 是留给
	// 「控制台要挪出内网」那天的开关。
	MTLSEnabled  bool
	SessionTTL   time.Duration
	OpsBotToken  string
	WebRoot      string
}

type Agent struct {
	MasterAddr string
	NodeID     string
	Token      string
	StateDir   string
	CaddyAdmin string
}

func LoadMaster() (Master, error) {
	c := Master{
		DatabaseURL: env("EC_DATABASE_URL", "postgres://localhost:5432/edge_controller?sslmode=disable"),
		// 默认绑回环而不是 0.0.0.0。ADR-0013 把「只绑内网」定为准入的一半，
		// 而一个默认对全网监听的控制面，装错一次就永远错着。
		HTTPAddr:    env("EC_HTTP_ADDR", "127.0.0.1:8080"),
		GRPCAddr:    env("EC_GRPC_ADDR", "0.0.0.0:9000"),
		MTLSEnabled: envBool("EC_MTLS", false),
		SessionTTL:  time.Duration(envInt("EC_SESSION_TTL_HOURS", 12)) * time.Hour,
		OpsBotToken: os.Getenv("EC_OPS_BOT_TOKEN"),
		WebRoot:     env("EC_WEB_ROOT", "web/dist"),
	}

	key := os.Getenv("EC_SECRET_KEY")
	if key == "" {
		return c, fmt.Errorf("EC_SECRET_KEY 未设置：DNS / Lark 凭证与 CA 私钥都用它做 AES-GCM 加密，没有它主控不该启动")
	}
	if len(key) < 32 {
		return c, fmt.Errorf("EC_SECRET_KEY 太短（%d 字节），至少 32", len(key))
	}
	c.SecretKey = []byte(key)
	return c, nil
}

func LoadAgent() (Agent, error) {
	c := Agent{
		MasterAddr: os.Getenv("EC_MASTER_ADDR"),
		NodeID:     os.Getenv("EC_NODE_ID"),
		Token:      os.Getenv("EC_ENROLL_TOKEN"),
		StateDir:   env("EC_STATE_DIR", "/var/lib/edge-agent"),
		CaddyAdmin: env("EC_CADDY_ADMIN", "http://127.0.0.1:2019"),
	}
	if c.MasterAddr == "" {
		return c, fmt.Errorf("EC_MASTER_ADDR 未设置")
	}
	if c.NodeID == "" {
		return c, fmt.Errorf("EC_NODE_ID 未设置")
	}
	return c, nil
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(k string, def int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
