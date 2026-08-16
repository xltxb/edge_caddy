// Package dnsprovider 是 DNS 服务商的客户端。
//
// 两个用途共用一份实现与一份凭据：
//
//	ACME DNS-01 挑战  —— 写/删 _acme-challenge 的 TXT 记录（工单 #13）
//	解析调度          —— 按线路与权重管理 A 记录（工单 #15）
//
// 不用 lego 自带的 DNS provider：它们只做 TXT，而调度需要完整的记录管理。
// 两套客户端意味着两份凭据处理，而凭据处理是最不该有第二份实现的地方。
//
// 凭据**只出现在请求头里**，绝不进 URL：URL 会进代理日志、进服务商的访问日志，
// 也会在出错时被原样打进我们自己的日志。
package dnsprovider

import (
	"context"
	"errors"
	"fmt"
)

// ErrNotSupported 表示该服务商不具备这项能力。
//
// 如实说「不支持」比悄悄降级好：悄悄按等权重写下去，人会以为权重配好了，
// 而实际流量是平均分的——直到某台机器被打爆才发现。
var ErrNotSupported = errors.New("该 DNS 服务商不支持此能力")

// Line 是解析线路。取值与 DNSPod 的线路概念对应（PRD §4）。
type Line string

const (
	LineDefault  Line = "默认"
	LineTelecom  Line = "电信"
	LineUnicom   Line = "联通"
	LineMobile   Line = "移动"
	LineTaiwan   Line = "台湾"
	LineOverseas Line = "境外"
)

// AllLines 是调度界面上的分组顺序。
var AllLines = []Line{LineDefault, LineTelecom, LineUnicom, LineMobile, LineTaiwan, LineOverseas}

// ARecord 是线上实际存在的一条 A 记录。
type ARecord struct {
	// ID 是服务商侧的记录 ID，改动与删除都靠它。
	ID string
	// Sub 是子域名，根域用 "@"。
	Sub    string
	Value  string
	Line   Line
	Weight int
	TTL    int
}

// Target 是调度计划里的一个目标。
type Target struct {
	NodeID string
	IP     string
	Line   Line
	Weight int
}

// TXTProvider 是 ACME DNS-01 需要的最小能力。
type TXTProvider interface {
	SetTXT(ctx context.Context, fqdn, value string) error
	RemoveTXT(ctx context.Context, fqdn, value string) error
}

// Provider 是完整能力。不支持的部分返回 ErrNotSupported。
type Provider interface {
	TXTProvider
	// Name 是服务商标识，用于审计与报错。
	Name() string
	// SupportsLines 报告是否有 ISP 线路概念。
	SupportsLines() bool
	// SupportsWeights 报告是否支持加权解析。
	SupportsWeights() bool
	// ListA 读回线上实际的 A 记录。界面靠它显示「库里的权重」与「线上实际解析」
	// 的差异——改了权重却没生效时，不看线上就无从察觉。
	ListA(ctx context.Context, domain string) ([]ARecord, error)
	// ApplyPlan 把调度计划落到线上。
	ApplyPlan(ctx context.Context, domain string, targets []Target) error
}

// apiError 是服务商返回的错误。
//
// 它刻意**不包含**请求内容：报错会进日志、进告警、进工单，而请求头里有凭据。
type apiError struct {
	provider string
	status   int
	code     string
	message  string
}

func (e *apiError) Error() string {
	if e.code != "" {
		return fmt.Sprintf("%s 返回错误（HTTP %d，code %s）：%s", e.provider, e.status, e.code, e.message)
	}
	return fmt.Sprintf("%s 返回错误（HTTP %d）：%s", e.provider, e.status, e.message)
}
