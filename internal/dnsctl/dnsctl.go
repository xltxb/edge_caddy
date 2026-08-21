// Package dnsctl 把算好的解析安排落到 DNS 服务商上。
//
// # 两家服务商的能力并不对等
//
// PRD §4 把 Cloudflare 与 DNSPod 并列为「分线路权重解析调度」的服务商，
// 但它们能做的事差得很远：
//
//   - **DNSPod** 原生支持线路（电信/联通/移动/境外…）与权重，这是它的核心功能。
//   - **Cloudflare 的 DNS 记录没有权重字段，也没有线路概念。** 要做加权调度得上
//     Load Balancing——另一套 API、额外付费，而且它的地理维度是国家/大洲，
//     **中国的 ISP 线路在那边根本表达不了**。
//
// 因此 Cloudflare 适配把 ct/cu/cm 三条线塌缩成「中国」。三者权重不同时它
// **明确报错**而不是取个平均值——给出一个用户没要过的配置，比拒绝更糟。
//
// # 关于可信度
//
// 这两个适配器是按公开 API 文档写的，**没有对真实账号验证过**。
// 单测用的是模拟服务端（issue #21 的验收要求如此，也因为拿真账号试探
// 一个改 zone 的接口不是个好主意）。线路名到服务商标识的映射尤其值得
// 在接入真账号时先用只读接口核对一遍。
package dnsctl

import (
	"context"
	"fmt"

	"github.com/xltxb/edge_caddy/internal/dnssched"
)

// Caps 说明一个服务商实际能做到什么。它会被 GET /dns/weights 回给前端，
// 让界面能把做不到的输入框置灰并说明原因，而不是让人配了个没有效果的数字。
type Caps struct {
	Kind string `json:"kind"`
	// Lines 是它能真正区分的线路码。做不到的线路会被塌缩到 Collapse 里说明的那一组。
	Lines []string `json:"lines"`
	// Weights 表示它能不能表达权重。false 时权重只有「>0 = 在解析里」的意义。
	Weights bool `json:"weights"`
	// Notes 是给人看的说明，直接呈在界面上。
	Notes string `json:"notes"`
}

type Provider interface {
	Caps() Caps
	// Sync 把一份安排落到服务商上。它必须是幂等的：同一份 Plan 反复 Sync
	// 不该产生重复记录——自愈会在节点抖动时反复调它。
	Sync(ctx context.Context, plan dnssched.Plan) error
}

// ErrCapability 表示这份安排超出了该服务商的能力。
//
// 单独一个错误类型，是为了让上层能把它翻成「你配的东西这家服务商做不到」，
// 而不是笼统的「下游失败」——后者会让人去查网络、查凭证，而问题根本不在那儿。
type ErrCapability struct{ Reason string }

func (e *ErrCapability) Error() string { return e.Reason }

func capErr(format string, a ...any) error {
	return &ErrCapability{Reason: fmt.Sprintf(format, a...)}
}
