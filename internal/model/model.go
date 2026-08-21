// Package model 是配置资源的领域类型。术语见 CONTEXT.md。
package model

import "encoding/json"

// Route 是一个对外域名到一个回源地址的映射，附带处置方式、请求体上限、压缩等策略。
type Route struct {
	Domain    string   `json:"domain"`
	Upstream  string   `json:"upstream"`   // host:port
	BlockMode string   `json:"block_mode"` // abort | 403 | 404
	MTLS      bool     `json:"mtls"`       // 回源 mTLS：边缘向源站出示客户端证书（ADR-0008）
	Compress  bool     `json:"compress"`
	BodyMax   string   `json:"body_max"` // 人类可读，如 "5MB"；渲染器转成字节数
	Whitelist []string `json:"whitelist"`
	Version   int      `json:"version"` // 0 = 尚未下发到任何节点
}

// 处置方式：请求未通过访问规则时的响应方式。
// 默认静默断连，不暴露服务是否存在。
const (
	BlockAbort = "abort"
	Block403   = "403"
	Block404   = "404"
)

// Rule 是挂在一个或多个域名上的准入条件。
// **未绑定域名的规则不生效**——那是半成品状态，不是「对所有域名生效」。
type Rule struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Enabled bool     `json:"enabled"`
	ApplyTo []string `json:"apply_to"`
	Version int      `json:"version"`
	Spec    RuleSpec `json:"spec"`

	// Secret 是服务密钥规则的共享密钥。它**不参与 JSON 序列化**——
	// spec 会被 GET /rules 原样返回，而凭证只写入不回显（PRD §7）。
	Secret string `json:"-"`
}

// RuleSpec 是三种规则类型字段的并集。哪些字段有意义由 Type 决定。
//
// 注意这里**没有** caddy-jwt 那套字段：JWT 与服务密钥的验签由 Agent 的校验端点
// 用 Go 完成，Caddy 只做 forward_auth 委托（ADR-0003）。照插件的字段名设计
// 会让人以为我们装了那个插件。
type RuleSpec struct {
	// ip_whitelist
	IPs []string `json:"ips,omitempty"`

	// service_secret
	Header           string `json:"header,omitempty"`
	Algo             string `json:"algo,omitempty"`
	TTLSeconds       int    `json:"ttl_s,omitempty"`
	ReplayProtection bool   `json:"replay_protection,omitempty"`
	SecretConfigured bool   `json:"secret_configured,omitempty"`

	// jwt_bearer
	Issuer      string `json:"iss,omitempty"`
	Audience    string `json:"aud,omitempty"`
	JWKSURL     string `json:"jwks_url,omitempty"`
	SkewSeconds int    `json:"skew_s,omitempty"`
}

const (
	RuleIPWhitelist   = "ip_whitelist"
	RuleServiceSecret = "service_secret"
	RuleJWTBearer     = "jwt_bearer"
)

// Policy 是全局策略。spec 的字段清单以高保真设计稿为准，
// 因此这里保持为原始 JSON —— 写死在 Go 结构体里只会让两边同时改。
type Policy struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Version int             `json:"version"`
	Spec    json.RawMessage `json:"spec"`
}

const (
	PolicyTLS = "tls"
	PolicyLog = "log"
)

// VerifyRule 是 Agent 校验端点需要的验签材料。
//
// 它随下发经隧道单独送达，**不进 Caddy 配置**：Caddy 的 Admin API 能读回整份
// 运行配置，共享密钥放进去等于摆在一个可读接口后面（proto 里 PushConfig
// 的注释说明了为什么这个和证书私钥不同——那个没得选，这个有）。
type VerifyRule struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Header string `json:"header,omitempty"`
	TTLSec int    `json:"ttl_s,omitempty"`
	Replay bool   `json:"replay,omitempty"`
	Secret string `json:"secret,omitempty"`

	Issuer   string `json:"iss,omitempty"`
	Audience string `json:"aud,omitempty"`
	JWKSURL  string `json:"jwks_url,omitempty"`
	SkewSec  int    `json:"skew_s,omitempty"`
}
