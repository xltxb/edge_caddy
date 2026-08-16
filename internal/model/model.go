// Package model 是领域模型。术语以仓库根目录的 CONTEXT.md 为准。
//
// 这里只放数据形状与它们自身的不变量，不放渲染、存储、传输的任何知识——
// 渲染在 internal/render，存储在 internal/store。
package model

import "time"

// BlockAction 是请求未通过白名单时的处置方式。
//
// 取值与设计稿的分段控件一一对应（abort / 403 / 404），不引入第四种。
// abort 是默认：它直接切断 TCP，不返回任何 HTTP 状态码，扫描器嗅探不到
// 应用是否存在；403/404 则等于承认「这个域名后面有服务」。
type BlockAction string

const (
	BlockAbort BlockAction = "abort"
	Block403   BlockAction = "403"
	Block404   BlockAction = "404"
)

// Valid 判断是否为已知处置方式。未知值必须在入口拒绝，
// 而不是渲染时兜底成 abort——兜底会让写错的配置看起来生效了。
func (b BlockAction) Valid() bool {
	return b == BlockAbort || b == Block403 || b == Block404
}

// AllowAllCIDR 是「放行所有来源」的白名单写法。
//
// 设计稿的 caddyJSON 对它有专门判定：白名单恰好只有这一条时**不渲染**
// 任何拒绝分支，而不是渲染一个「谁都匹配」的 not 匹配器。两者行为等价，
// 但前者的产出干净得多，也让 diff 里看不到无意义的噪声。
const AllowAllCIDR = "0.0.0.0/0"

// Route 是一个对外域名到一个回源地址的映射。
//
// 域名是主键（后端文档 §3 的 proxy_routes.domain PRIMARY KEY）。这意味着
// 改域名等于换一条路由：旧域名的版本号与历史不会跟着走。这是文档的选择，
// 不是疏漏——工作台的资源键 route:api.example.com 也建立在同一前提上。
type Route struct {
	Domain   string      `json:"domain"`
	Upstream string      `json:"upstream"` // host:port
	Block    BlockAction `json:"block"`
	MTLS     bool        `json:"mtls"` // 回源时出示客户端证书，非「要求访问者出证书」
	Compress bool        `json:"compress"`
	// BodyMax 以人类可读形式保存（"5MB"）。渲染时必须转成字节数：
	// Caddy 的 request_body.max_size 是 int64，给字符串会被整份拒绝。
	BodyMax string `json:"body_max"`
	// Whitelist 是允许的来源 IP / CIDR。空表示不生成拒绝分支（不是「谁都不许进」）。
	Whitelist []string `json:"wl"`
	// Version 为 0 表示尚未下发到任何节点。
	Version int `json:"ver"`
}

// RuleType 是访问规则的类型。
type RuleType string

const (
	RuleIPWhitelist   RuleType = "ip_whitelist"
	RuleServiceSecret RuleType = "service_secret"
	RuleJWTBearer     RuleType = "jwt_bearer"
)

// RuleSpec 承载各类型规则各自的字段。
//
// 这些字段不由 Caddy 直接校验——边缘的真实验签发生在 Agent 的校验端点上
// （见 docs/adr/0003）。渲染器只负责把受保护的域名接到 forward_auth 上。
type RuleSpec struct {
	IPs []string `json:"ips,omitempty"`

	Header string `json:"header,omitempty"`
	Algo   string `json:"algo,omitempty"`
	TTLSec int    `json:"ttl,omitempty"`
	Replay bool   `json:"replay,omitempty"`

	Issuer   string `json:"issuer,omitempty"`
	Audience string `json:"audience,omitempty"`
	JWKS     string `json:"jwks,omitempty"`
	SkewSec  int    `json:"skew,omitempty"`
}

// AccessRule 是挂在若干域名上的准入条件。
type AccessRule struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Type    RuleType `json:"type"`
	Enabled bool     `json:"enabled"`
	Spec    RuleSpec `json:"spec"`
	// ApplyTo 为空表示不绑定任何域名，因而不生效。
	// 这是「建了一半」的状态，不是「对所有域名生效」。
	ApplyTo []string `json:"apply_to"`
	Version int      `json:"ver"`
}

// Node 是一台边缘节点在主控侧的记录。
type Node struct {
	ID         string    `json:"id"`
	City       string    `json:"city"`
	Vendor     string    `json:"vendor"`
	Line       string    `json:"line"`
	PublicIP   string    `json:"ip"`
	Status     string    `json:"status"` // ok / warn / down
	CfgVersion string    `json:"cfg"`
	DNSEnabled bool      `json:"dns"`
	LastHB     time.Time `json:"-"`
	CreatedAt  time.Time `json:"-"`
}

// Draft 是叠加在基线之上、尚未下发的改动。
//
// 全局可见：任何人都能看到别人正在改什么。但一次下发只携带**本次勾选**的
// 草稿键，未勾选的仍留在草稿里（见 CONTEXT.md 的「下发」）。
type Draft struct {
	ResKey    string         `json:"res_key"` // route:api.example.com
	Patch     map[string]any `json:"patch"`
	UpdatedBy string         `json:"updated_by"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Deploy 是一次下发的记录。Snapshot 是当次的全量渲染快照，回滚以它为源。
type Deploy struct {
	ID         int64          `json:"id"`
	CfgVersion string         `json:"cfg_version"`
	Operator   string         `json:"operator"`
	ResKeys    []string       `json:"res_keys"`
	Snapshot   map[string]any `json:"snapshot"`
	OKCount    int            `json:"ok_count"`
	FailCount  int            `json:"fail_count"`
	CreatedAt  time.Time      `json:"created_at"`
}

// DeployResult 是某次下发在单个节点上的结果。
//
// Detail 成功时是耗时（"31ms"），失败时是原因原文。失败原文直接来自 Caddy，
// 不做归类——实测 Caddy 对语法错误、未知 handler、字段类型错、端口占用
// 一律返回 500，任何基于状态码的归类都是错的（见 docs/adr/0005）。
type DeployResult struct {
	DeployID int64  `json:"-"`
	NodeID   string `json:"node"`
	State    string `json:"state"` // ok / fail
	Detail   string `json:"res"`
}
