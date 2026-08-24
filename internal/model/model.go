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

	// request_filter：按请求特征拦。每一条都是「命中就拦」。
	//
	// **它们之间是或的关系**，一条规则里的多个条件命中任意一个就拦下。
	// 想要「且」就配成两条规则——那让每一条在界面上都能单独开关，
	// 而一个复合条件是拆不开的，出问题时也说不清是哪一半命中的。
	Filters []Filter `json:"filters,omitempty"`

	// rate_limit
	//
	// **令牌桶**：桶容量 Requests，每 WindowSeconds 补满一次。
	// 于是它同时表达了两件事——持续速率 Requests/WindowSeconds，
	// 以及**允许的突发** Requests。
	//
	// 滑动窗口计数器只表达前者，而真实流量总是成簇的：
	// 一个正常用户打开页面会并发十几个请求，按纯速率算它一定会被误伤。
	Requests      int `json:"requests,omitempty"`
	WindowSeconds int `json:"window_s,omitempty"`

	// RateKey 是按什么计数：ip | ip_path。
	//
	// ip_path 让「同一个 IP 猛刷登录接口」和「它正常浏览别的页面」互不影响，
	// 代价是内存里的桶数量乘以路径基数——**而路径是攻击者能控制的**。
	// 所以 ip_path 只在配了 request_filter 限定路径时才有意义，
	// 默认是 ip。
	RateKey string `json:"rate_key,omitempty"`

	// geo_block
	//
	// **两个方向二选一，不是一个开关。** 理由与 IP 黑白名单相同：
	// 默认方向相反，合成一个之后「清空清单」的后果是
	// 「谁都进不来」和「谁都能进」，而界面上它们长得一样。
	//
	// 国家用 ISO 3166-1 alpha-2（CN / US / HK…）。
	GeoMode      string   `json:"geo_mode,omitempty"` // block | allow
	GeoCountries []string `json:"geo_countries,omitempty"`
}

// Filter 是一条请求特征。
//
// **它是「拦」不是「放」**：命中即按路由的处置方式断掉。
// 白名单那一类走 ip_whitelist，两者的默认方向相反，混在一起说不清。
type Filter struct {
	// Field 是看请求的哪一部分：path | user_agent | referer | header | query
	Field string `json:"field"`
	// Name 只在 Field 为 header / query 时有意义（要看哪个头 / 哪个参数）。
	Name string `json:"name,omitempty"`
	// Op 是怎么比：contains | prefix | suffix | equals | regex
	Op string `json:"op"`
	// Value 是比什么。regex 用 Go 的语法（Caddy 的匹配器也是 RE2）。
	Value string `json:"value"`
}

const (
	RuleIPWhitelist   = "ip_whitelist"
	RuleServiceSecret = "service_secret"
	RuleJWTBearer     = "jwt_bearer"

	// RuleIPBlacklist 是白名单的反面。
	//
	// **两者不能合成一个「IP 规则」**：默认方向相反——白名单是「只放这些」，
	// 黑名单是「只拦这些」。合成一个之后，一条清空了 IP 的规则
	// 在两种语义下的后果是「谁都进不来」和「谁都能进」，
	// 而界面上它们长得一模一样。
	RuleIPBlacklist = "ip_blacklist"

	// RuleRequestFilter 按请求特征拦（UA / 路径 / 请求头 / 查询参数）。
	RuleRequestFilter = "request_filter"

	// RuleRateLimit 是限流 / CC 防护。
	//
	// **它走 Agent 的校验端点**：官方 Caddy 没有限流模块，而这条委托
	// （ADR-0003）本来就是为「Caddy 做不到的准入判断」建的。
	//
	// **计数是每节点各算各的。** 三台节点、每台限 100，全局实际是 300 ——
	// 这一点必须在界面上说出来：一个人按「我要限 100」去配，
	// 拿到的是 300，而没有任何地方会告诉他。
	RuleRateLimit = "rate_limit"

	// RuleGeoBlock 是地域封禁 / 放行。
	//
	// **也走校验端点**：官方 Caddy 没有 GeoIP 模块，而查库要一份
	// MaxMind 的 mmdb —— 那是这套系统的第一个外部数据依赖。
	//
	// 库由主控持有并下发（与证书同一条路），**节点不自己去 MaxMind 下载**：
	// 那要把 license key 散到每台边缘机器上，而边缘机器是最可能被拿下的那些。
	RuleGeoBlock = "geo_block"
)

// FilterFieldOps 是**每个 field 允许哪些 op**。
//
// # 为什么是一张表，而不是「全局 op 集合 + 特例」
//
// 第一版是那样写的：所有 op 都放行，再加一个
// `if f.Field == "query" && f.Op != "equals"` 的特例。
//
// 那种形状的问题是**特例不会提醒下一个人**：加第八种 field 时，
// 没有任何东西会让他想起「要不要也给它收窄」。而界面那边如果按
// 「随 field 变的下拉」实现，两边就在加第八种时分叉——
// 界面给出一个后端会拒的选项，或者藏起一个后端接受的。
//
// 表还有第二个作用：**它报得出去**（GET /rules 的 filter_fields），
// 于是界面的下拉是数据驱动的，而不是抄一份。抄的那份不会有人去校对。
//
// query 只有 equals 的理由：Caddy 的 query 匹配器只比精确值。
// 悄悄当成 equals 的话，一条 contains 规则会变成精确匹配——拦不到它该拦的。
var FilterFieldOps = map[string][]string{
	"path":       {"contains", "prefix", "suffix", "equals", "regex"},
	"user_agent": {"contains", "prefix", "suffix", "equals", "regex"},
	"referer":    {"contains", "prefix", "suffix", "equals", "regex"},
	"header":     {"contains", "prefix", "suffix", "equals", "regex"},
	"query":      {"equals"},
}

// FilterFieldsNeedingName 是那些还要指明「看哪一个」的 field。
var FilterFieldsNeedingName = map[string]bool{"header": true, "query": true}

// FilterOpAllowed 说这个 field 收不收这个 op。
func FilterOpAllowed(field, op string) bool {
	for _, x := range FilterFieldOps[field] {
		if x == op {
			return true
		}
	}
	return false
}

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

	// 限流。**这些值下发到节点上，由 Agent 各自计数** ——
	// 三台节点每台限 100，全局实际是 300。
	Requests  int    `json:"requests,omitempty"`
	WindowSec int    `json:"window_s,omitempty"`
	RateKey   string `json:"rate_key,omitempty"`

	// 地域。**下发到节点，由 Agent 查本机的 mmdb**。
	GeoMode      string   `json:"geo_mode,omitempty"`
	GeoCountries []string `json:"geo_countries,omitempty"`
}
