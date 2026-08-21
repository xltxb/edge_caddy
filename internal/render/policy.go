package render

import (
	"encoding/json"
	"fmt"
)

// 全局策略的默认值。
//
// **这里是唯一的一份。** GET /policies/:id 在 spec 缺字段时用它补齐，
// 于是界面显示的就是节点上实际生效的——而不是一片空白让人猜。
// 契约 §6.3 的 seed 值必须与这里一致，不一致就是文档和实现各说各话。
var (
	DefaultTLSPolicy = TLSPolicy{
		CA: "letsencrypt", KeyType: "p256", MinVersion: "1.2",
		HTTP3: true, HSTS: true, HSTSMaxAge: 63072000, OCSP: false,
	}
	DefaultLogPolicy = LogPolicy{
		Format: "json", Level: "INFO", RollSize: 50, RollKeep: 5,
		StripHeaders: true,
	}
)

type TLSPolicy struct {
	// CA / Email / KeyType 是**主控**签发证书时用的参数，不下发给节点。
	CA      string `json:"ca,omitempty"`
	Email   string `json:"email,omitempty"`
	KeyType string `json:"key_type,omitempty"`

	// 以下才是真正渲染进节点配置的。
	MinVersion string `json:"min_version,omitempty"`
	HTTP3      bool   `json:"http3"`
	HSTS       bool   `json:"hsts"`
	HSTSMaxAge int    `json:"hsts_max_age,omitempty"`
	OCSP       bool   `json:"ocsp"`
}

type LogPolicy struct {
	Format       string `json:"format,omitempty"`
	Level        string `json:"level,omitempty"`
	RollSize     int    `json:"roll_size,omitempty"`
	RollKeep     int    `json:"roll_keep,omitempty"`
	StripHeaders bool   `json:"strip_headers"`

	// 限流三项。**官方 Caddy 没有限流模块**（2.11.4 的 132 个标准模块里
	// 一个都没有，caddy-ratelimit 是插件），因此 RateLimit 为 true 时
	// 校验会拒绝——一个开着却没有效果的限流开关，比一个明说「做不到」
	// 的报错危险得多。
	RateLimit bool `json:"rate_limit"`
	RateRPS   int  `json:"rate_rps,omitempty"`
	RateBurst int  `json:"rate_burst,omitempty"`
}

// Policies 是渲染时用到的两条全局策略。
type Policies struct {
	TLS TLSPolicy
	Log LogPolicy
}

// ParsePolicies 把库里的原始 spec 解成结构，缺字段用默认值补齐。
func ParsePolicies(tlsSpec, logSpec json.RawMessage) (Policies, error) {
	out := Policies{TLS: DefaultTLSPolicy, Log: DefaultLogPolicy}
	if len(tlsSpec) > 0 {
		if err := json.Unmarshal(tlsSpec, &out.TLS); err != nil {
			return out, fmt.Errorf("解析 TLS 策略: %w", err)
		}
	}
	if len(logSpec) > 0 {
		if err := json.Unmarshal(logSpec, &out.Log); err != nil {
			return out, fmt.Errorf("解析日志策略: %w", err)
		}
	}
	return out, nil
}

func validatePolicies(p Policies) []Issue {
	var issues []Issue

	switch p.TLS.MinVersion {
	case "", "1.2", "1.3":
	default:
		issues = append(issues, Issue{"global:tls", "spec.min_version", "只能是 1.2 或 1.3"})
	}
	switch p.TLS.KeyType {
	case "", "p256", "p384", "rsa2048":
	default:
		issues = append(issues, Issue{"global:tls", "spec.key_type", "只能是 p256 / p384 / rsa2048"})
	}
	switch p.TLS.CA {
	case "", "letsencrypt", "zerossl":
	default:
		issues = append(issues, Issue{"global:tls", "spec.ca", "只能是 letsencrypt 或 zerossl"})
	}

	switch p.Log.Format {
	case "", "json", "console":
	default:
		issues = append(issues, Issue{"global:log", "spec.format", "只能是 json 或 console"})
	}
	switch p.Log.Level {
	case "", "DEBUG", "INFO", "WARN", "ERROR":
	default:
		issues = append(issues, Issue{"global:log", "spec.level", "只能是 DEBUG / INFO / WARN / ERROR"})
	}

	if p.Log.RateLimit {
		// 宁可拒绝也不静默忽略。这与回源 mTLS 当初的处理一致：
		// 一个开着却没有效果的安全开关，比一个明说「还没做」的报错危险得多。
		issues = append(issues, Issue{"global:log", "spec.rate_limit",
			"官方 Caddy 没有限流模块（caddy-ratelimit 是插件），当前做不到；" +
				"要用它就得自建 Caddy 二进制，而那会推翻「节点跑官方包」这个前提"})
	}
	return issues
}

// tlsProtocolMin 把 1.2 / 1.3 翻成 Caddy 的写法。
func tlsProtocolMin(v string) string {
	switch v {
	case "1.3":
		return "tls1.3"
	default:
		return "tls1.2"
	}
}

// serverProtocols 决定这台 server 支持哪些协议。
// HTTP/3 需要额外放行 443/udp——那是部署脚本的事，配置这边只管开关。
func serverProtocols(p Policies, tls bool) []string {
	if !tls {
		// 明文 server 上 h2 与 h3 都要求 TLS，开了也用不上。
		return []string{"h1"}
	}
	if p.TLS.HTTP3 {
		return []string{"h1", "h2", "h3"}
	}
	return []string{"h1", "h2"}
}

// responseHeaderHandler 产出 HSTS 与去掉指纹响应头的 handler。
// 两件事共用一个 handler，因为 Caddy 的 headers 模块本来就同时管增删。
func responseHeaderHandler(p Policies, tls bool) map[string]any {
	set := map[string]any{}
	var del []string

	if p.TLS.HSTS && tls {
		// HSTS 只在 TLS 上有意义：在明文响应里发它，浏览器会忽略，
		// 而它会让人以为已经生效了。
		age := p.TLS.HSTSMaxAge
		if age <= 0 {
			age = DefaultTLSPolicy.HSTSMaxAge
		}
		set["Strict-Transport-Security"] = []string{
			fmt.Sprintf("max-age=%d; includeSubDomains", age),
		}
	}
	if p.Log.StripHeaders {
		del = append(del, "Server", "X-Powered-By")
	}
	if len(set) == 0 && len(del) == 0 {
		return nil
	}

	resp := map[string]any{
		// deferred：Server 头是 Caddy 在写响应时才加的，不延后就删不掉。
		"deferred": true,
	}
	if len(set) > 0 {
		resp["set"] = set
	}
	if len(del) > 0 {
		resp["delete"] = del
	}
	return map[string]any{"handler": "headers", "response": resp}
}

// loggingApp 渲染 Caddy 的 logging app。
func loggingApp(p Policies) map[string]any {
	encoder := "json"
	if p.Log.Format == "console" {
		encoder = "console"
	}
	level := p.Log.Level
	if level == "" {
		level = DefaultLogPolicy.Level
	}
	return map[string]any{
		"logs": map[string]any{
			"default": map[string]any{
				"encoder": map[string]any{"format": encoder},
				"level":   level,
			},
		},
	}
}
