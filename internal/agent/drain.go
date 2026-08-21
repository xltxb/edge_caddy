package agent

import (
	"context"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
)

// drainDeadline 是主控没给超时时用的默认值。
const drainDeadline = 30 * time.Second

// drainPoll 是两次数连接之间的间隔。
//
// 不能太密：countConns 会把本机全部 TCP 连接列一遍，在一台正扛着流量的边缘
// 机器上那不是免费的，而下线恰恰发生在还有流量的时候。
const drainPoll = 500 * time.Millisecond

// waitDrained 等已建立的连接降到 0，或者到点。
//
// 单独一个函数、连接数由参数传进来，是为了能不起真机器就测到那两个边界
// （立刻为 0、到点仍不为 0）—— 而那两个边界正是这段代码唯一会出错的地方。
//
// **它不阻止新连接。** 解析虽已摘掉，DNS 有 TTL，缓存在各级递归里，
// 一段时间内仍会有新连接进来（CONTEXT.md「排空」）。所以返回的 remaining
// 说的是**回报那一刻**的连接数，不是「从此再也没有请求」。
func waitDrained(ctx context.Context, count func(context.Context) uint32,
	timeout, poll time.Duration) (drained bool, remaining uint32) {

	if timeout <= 0 {
		timeout = drainDeadline
	}
	if poll <= 0 {
		poll = drainPoll
	}
	deadline := time.Now().Add(timeout)

	for {
		remaining = count(ctx)
		if remaining == 0 {
			return true, 0
		}
		// 先判到点再睡：否则最后一次睡完才发现超时，白等一个周期，
		// 而报上去的数字是睡之前那个 —— 比实际旧了一个周期。
		if !time.Now().Before(deadline) {
			return false, remaining
		}
		select {
		case <-ctx.Done():
			// 隧道断了或进程要退了。这时报的数字仍然是真的，
			// 只是没等到 0 —— 与超时同一个形状。
			return false, remaining
		case <-time.After(poll):
		}
	}
}

// handleDrain 排空本机连接并回报。
//
// 它**不关 Caddy、不断隧道**：那两件事分别是人的决定和主控的决定。
// Agent 在这里只做一件事——等，然后如实说还剩多少。
func (a *Agent) handleDrain(ctx context.Context, stream edgev1.EdgeTunnel_ChannelClient, d *edgev1.Drain) {
	timeout := time.Duration(d.GetTimeoutMs()) * time.Millisecond
	a.log.Info("开始排空连接", "timeout", timeout)

	drained, remaining := waitDrained(ctx, a.metrics.countConns, timeout, drainPoll)
	a.log.Info("排空结束", "drained", drained, "remaining", remaining)

	if err := stream.Send(&edgev1.AgentMsg{M: &edgev1.AgentMsg_DrainResult{
		DrainResult: &edgev1.DrainResult{
			Id: d.GetId(), Drained: drained, Remaining: remaining,
		},
	}}); err != nil {
		a.log.Error("回报排空结果失败", "err", err)
	}
}
