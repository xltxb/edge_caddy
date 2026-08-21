// Package model 是配置资源的领域类型。术语见 CONTEXT.md。
package model

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
