package agent

import (
	"sync"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
)

// sender 是一条流的写入端。抽出来只为让串行化能被单独测——
// 真流与测试里的替身都满足它。
type sender interface {
	Send(*edgev1.AgentMsg) error
}

// tunnelWriter 把一条隧道上的所有发送串行化。
//
// **gRPC 的流不允许并发 Send**，而 Agent 有五条并发的发送路径：心跳、日志、
// 证书回执、排空回执，以及读循环自己回的下发结果与探活结果。主控侧
// （tunnel/session.go）为同一条约束建了 writeLoop，Agent 侧此前什么都没有
// ——一次下发回执撞上心跳就够了（issue #37）。走 WebSocket 那条路时底层还叠了
// gorilla 的「不允许并发写」。
//
// # 为什么是锁，不是 out chan + writeLoop
//
// 主控那边用 channel，因为它的发送方要的是「投递出去就不管了」。Agent 这边
// 不是：logLoop 发送失败时要把那批日志 putBack 回缓冲，等隧道恢复后重发。
// channel 是投递即返回，那个错误拿不回来，于是「隧道断了就丢一批日志」会
// 静默地成为新行为，而它看起来一切正常。
//
// 锁让每一条现有的错误路径原封不动：调用点拿到的还是同一个 `Send(msg) error`。
//
// # 为什么 handler 收的是这个具体类型而不是 sender
//
// 收接口的话，把裸流传进某个 handler 仍然编得过，而那正是这个 bug 的形状：
// 一处漏网就够。收具体类型之后，编译器替我们守住「所有发送都经过这里」——
// 这条比任何注释都可靠，也比一条只测包装类型自己的测试可靠。
type tunnelWriter struct {
	mu sync.Mutex
	s  sender
}

func newTunnelWriter(s sender) *tunnelWriter { return &tunnelWriter{s: s} }

func (w *tunnelWriter) Send(m *edgev1.AgentMsg) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.s.Send(m)
}
