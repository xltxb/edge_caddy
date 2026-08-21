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
	MTLSEnabled bool
	SessionTTL  time.Duration
	OpsBotToken string
	WebRoot     string

	// Advertise 是主控对节点公布的地址，进服务端证书的 SAN，也拼进安装命令。
	// PRD 要求强制域名而非 IP，这里**不强制**——用 IP 也能跑。
	//
	// 原先这句话写着「属于 #20 的系统设置」，而 #20 已经关了，它没做。
	// 一个挂在已关工单上的欠条不会有人来兑现：那张单子关了，
	// 没有任何流程会再打开这一行。改挂 #24（待人工实现：硬校验还是只警告，
	// 取决于愿不愿意让现有用 IP 跑着的部署在升级时起不来）。
	Advertise string
	// EdgeHTTPListen 是渲染进节点配置的监听地址。生产是 ":80"。
	EdgeHTTPListen string
	// EdgeHTTPSListen 是 TLS server 的监听地址。只在主控持有证书时才渲染那台。
	EdgeHTTPSListen string
	// ACMEEmail / ACMEDirectory 是签发证书用的 ACME 账户。
	// Directory 留空即 Let's Encrypt 生产环境；首次接入建议先指向 staging——
	// 那边的速率限制宽得多，签废了也不心疼。
	ACMEEmail     string
	ACMEDirectory string
	// UpstreamCert / UpstreamKey 是节点回源时出示的客户端证书在**节点本机**的路径。
	UpstreamCert string
	UpstreamKey  string
	// VerifyAddr 是 Agent 校验端点在**节点**回环上的地址，渲染进 forward_auth。
	// 它是节点侧的事实，主控只是把它写进配置，所以两边要配一致。
	VerifyAddr string
}

type Agent struct {
	MasterAddr   string
	NodeID       string
	Token        string
	StateDir     string
	CaddyAdmin   string
	VerifyListen string
}

func LoadMaster() (Master, error) {
	c := Master{
		DatabaseURL: env("EC_DATABASE_URL", "postgres://localhost:5432/edge_controller?sslmode=disable"),
		// 默认绑回环而不是 0.0.0.0。ADR-0013 把「只绑内网」定为准入的一半，
		// 而一个默认对全网监听的控制面，装错一次就永远错着。
		HTTPAddr:        env("EC_HTTP_ADDR", "127.0.0.1:8080"),
		GRPCAddr:        env("EC_GRPC_ADDR", "0.0.0.0:9000"),
		MTLSEnabled:     envBool("EC_MTLS", false),
		SessionTTL:      time.Duration(envInt("EC_SESSION_TTL_HOURS", 12)) * time.Hour,
		OpsBotToken:     os.Getenv("EC_OPS_BOT_TOKEN"),
		WebRoot:         env("EC_WEB_ROOT", "web/dist"),
		Advertise:       env("EC_ADVERTISE", "127.0.0.1:9000"),
		EdgeHTTPListen:  env("EC_EDGE_HTTP_LISTEN", ":80"),
		VerifyAddr:      env("EC_VERIFY_ADDR", "127.0.0.1:2020"),
		EdgeHTTPSListen: env("EC_EDGE_HTTPS_LISTEN", ":443"),
		ACMEEmail:       os.Getenv("EC_ACME_EMAIL"),
		ACMEDirectory:   os.Getenv("EC_ACME_DIRECTORY"),
		UpstreamCert:    env("EC_UPSTREAM_CERT", "/var/lib/edge-agent/edge-mtls.crt"),
		UpstreamKey:     env("EC_UPSTREAM_KEY", "/var/lib/edge-agent/edge-mtls.key"),
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
		MasterAddr:   os.Getenv("EC_MASTER_ADDR"),
		NodeID:       os.Getenv("EC_NODE_ID"),
		Token:        os.Getenv("EC_ENROLL_TOKEN"),
		StateDir:     env("EC_STATE_DIR", "/var/lib/edge-agent"),
		CaddyAdmin:   env("EC_CADDY_ADMIN", "http://127.0.0.1:2019"),
		VerifyListen: env("EC_VERIFY_LISTEN", "127.0.0.1:2020"),
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
