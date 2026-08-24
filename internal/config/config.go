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

	// MTLSEnabled 目前**只能是 false**：翻开它主控会拒绝启动。
	//
	// ADR-0013 说 mTLS 以 `tls.Config.ClientAuth` 实现为一个默认关的开关，
	// 而**那一半从来没有写**。这个字段现在唯一的用处是决定 Cookie 的
	// Secure 标志——那与 mTLS 是两件事。
	//
	// **一个翻开之后什么也不做的安全开关，比没有这个开关危险得多**：
	// 人会以为控制台开着 mTLS，而它没有，**而这件事不会有任何症状**，
	// 直到有人真的去中间人。
	//
	// 所以它现在是一个会拒绝启动的开关（见 ValidateMTLS）：
	// 把一个静默的假象换成一个响亮的失败。真要用它，先把 ADR-0013 里
	// 那一半实现掉，连同它写明的「回环逃生口」——开着 mTLS 又弄丢证书时，
	// 回环上的监听必须仍然可用，否则会把唯一的运维人员锁在系统外面。
	MTLSEnabled bool

	// SecureCookie 决定会话 Cookie 带不带 Secure 标志。
	//
	// **它此前跟 MTLSEnabled 绑死**，而那是个错误的耦合：Secure 该由
	// 「控制台跑在 TLS 上」决定，跟 mTLS 开没开是两件事。
	// 前置 nginx 终止 TLS 时，主控自己看到的是 HTTP，而浏览器走的是 HTTPS
	// ——那时该设 EC_SECURE_COOKIE=1。
	//
	// 默认 false：ADR-0013 下首版绑内网 HTTP，硬写 true 会让 Cookie
	// 在 http:// 下根本不被存下来，现象是「登录成功但立刻又跳回登录页」。
	SecureCookie bool

	// TrustedProxies 是可信反代的地址，**只有它们发来的 X-Forwarded-For
	// 才作数**。
	//
	// 默认空 = 谁也不信，来源 IP 一律取连接的对端地址。
	//
	// 这不是保守，是修一个真问题：gin 默认信任所有代理，于是**任何能访问
	// 控制台的人发一个 XFF 头就能伪造审计日志里的来源 IP**——而审计是
	// ADR-0013 准入模型的三分之一。实测过：不配的话 `X-Forwarded-For: 1.2.3.4`
	// 会原样进审计。
	TrustedProxies []string
	SessionTTL     time.Duration
	OpsBotToken    string
	CertBotToken   string
	MaxMindKey     string
	WebRoot        string

	// Advertise 是主控对节点公布的地址，进服务端证书的 SAN，也拼进安装命令。
	//
	// **必须是域名，不是 IP**，启动时硬校验（#24，见 ValidateAdvertise）。
	// 这是一次明确的取舍：现有用 IP 跑着的部署升级时会起不来，人得先改配置。
	// 换来的是主控换地址那天不用挨台改节点。
	Advertise string
	// EdgeHTTPListen 是渲染进节点配置的监听地址。生产是 ":80"。
	EdgeHTTPListen string
	// EdgeHTTPSListen 是 TLS server 的监听地址。只在主控持有证书时才渲染那台。
	EdgeHTTPSListen string
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
		HTTPAddr:     env("EC_HTTP_ADDR", "127.0.0.1:8080"),
		GRPCAddr:     env("EC_GRPC_ADDR", "0.0.0.0:9000"),
		MTLSEnabled:  envBool("EC_MTLS", false),
		SecureCookie: envBool("EC_SECURE_COOKIE", false),
		// 逗号分隔，例如 EC_TRUSTED_PROXIES=127.0.0.1,::1
		TrustedProxies: splitList(os.Getenv("EC_TRUSTED_PROXIES")),
		SessionTTL:     time.Duration(envInt("EC_SESSION_TTL_HOURS", 12)) * time.Hour,
		OpsBotToken:    os.Getenv("EC_OPS_BOT_TOKEN"),
		CertBotToken:   os.Getenv("EC_CERT_BOT_TOKEN"),
		MaxMindKey:     os.Getenv("EC_MAXMIND_LICENSE_KEY"),
		WebRoot:        env("EC_WEB_ROOT", "web/dist"),
		// **不给默认值。** 任何默认值在生产上都是错的——没人的主控真叫那个名字——
		// 而一个能启动的错误默认值比起不来更危险：它会让人以为配好了，
		// 直到第一台节点连不上。与 EC_SECRET_KEY 同一条。
		Advertise:       os.Getenv("EC_ADVERTISE"),
		EdgeHTTPListen:  env("EC_EDGE_HTTP_LISTEN", ":80"),
		VerifyAddr:      env("EC_VERIFY_ADDR", "127.0.0.1:2020"),
		EdgeHTTPSListen: env("EC_EDGE_HTTPS_LISTEN", ":443"),
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

	// **两个 bot token 不能是同一个值。**
	//
	// 设成一样的话，比对时 ops-bot 那一支先命中，cert-bot 的路由白名单
	// 根本走不到 —— 人以为自己把外部平台收窄到了两个端点，
	// 而实际交出去的是整个控制面。
	//
	// **一个「配错了看不出来」的安全边界等于没有边界**，所以在启动时拒绝，
	// 而不是等到出事之后从审计里看出来。
	if c.OpsBotToken != "" && c.OpsBotToken == c.CertBotToken {
		return c, fmt.Errorf(
			"EC_OPS_BOT_TOKEN 与 EC_CERT_BOT_TOKEN 是同一个值 —— " +
				"那样 cert-bot 的端点白名单不会生效，外部平台拿到的是完整权限。两个要各生成一个")
	}

	// 放在最后：先把该报的凭据问题报完，再报这个。一次只让人改一样东西时，
	// 顺序就是他修复的顺序。
	if err := ValidateAdvertise(c.Advertise); err != nil {
		return c, err
	}
	if err := ValidateMTLS(c.MTLSEnabled); err != nil {
		return c, err
	}
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

// splitList 把逗号分隔的列表切开，去掉空白项。
//
// 空字符串返回 nil 而不是 []string{""} —— 后者会被 gin 当成一个
// 名为空串的可信代理，而那是个既不报错也不生效的状态。
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
