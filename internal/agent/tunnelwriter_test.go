package agent

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
)

// overlapSender 数「同一时刻有几个 Send 在里面」。
//
// **判据是重叠，不是次数。** 数次数答不了这个问题：一条被并发调用的流
// 和一条被串行调用的流，发出去的消息条数完全一样，区别只在有没有两个
// goroutine 同时待在 Send 里。所以这里在进出各记一笔，并在中间停一会儿——
// 不停的话窗口太窄，一次真实的并发也可能碰巧不重叠，而那会让这条测试
// 变成一条偶尔才生效的测试。
type overlapSender struct {
	mu       sync.Mutex
	inFlight int
	max      int
	total    int
}

func (o *overlapSender) Send(*edgev1.AgentMsg) error {
	o.mu.Lock()
	o.inFlight++
	o.total++
	if o.inFlight > o.max {
		o.max = o.inFlight
	}
	o.mu.Unlock()

	time.Sleep(2 * time.Millisecond)

	o.mu.Lock()
	o.inFlight--
	o.mu.Unlock()
	return nil
}

func (o *overlapSender) peak() (maxInFlight, total int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.max, o.total
}

// TestTunnelWriterSerializesConcurrentSends：包装之后，同一条流上不会有两个
// Send 重叠。
//
// gRPC 的流不允许并发 Send，主控侧 tunnel/session.go:17 为此专门建了 writeLoop
// 并把理由写在类型注释里。Agent 侧没有这层，而它有五条并发的发送路径
// （心跳、日志、证书回执、排空回执，以及读循环自己回的下发/探活结果）——
// issue #37。走 WebSocket 那条路时底层还叠了 gorilla 的「不允许并发写」。
func TestTunnelWriterSerializesConcurrentSends(t *testing.T) {
	o := &overlapSender{}
	w := newTunnelWriter(o)

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.Send(&edgev1.AgentMsg{})
		}()
	}
	wg.Wait()

	maxInFlight, total := o.peak()
	if total != 24 {
		t.Fatalf("只有 %d 条消息到达流上，装置本身就不对", total)
	}
	if maxInFlight > 1 {
		t.Errorf("同一时刻有 %d 个 Send 在同一条流上——gRPC 不允许", maxInFlight)
	}
}

// TestAgentSendPathsShareOneSerializedWriter：**真实的发送路径**并发跑，
// 也不会重叠。
//
// 上一条只证明包装类型自己是串行的；一个没人用的包装类型同样能让它绿。
// 这一条从 Agent 的两条真实发送路径进去（证书回执与探活回执，两者都会
// 立刻发一条），它们共用同一个写入端——与线上那条隧道的拓扑一致。
//
// 心跳与日志两条路径没有在这里驱动：logFlushInterval 是 5 秒，而心跳要先做
// 一次 200ms 的 CPU 采样。它们走的是同一个写入端，由类型系统保证——
// 所有 handler 收的都是 *tunnelWriter，裸流传不进去。
func TestAgentSendPathsShareOneSerializedWriter(t *testing.T) {
	o := &overlapSender{}
	w := newTunnelWriter(o)

	a := New(Config{
		NodeID: "node-hk-01",
		// 指向一个没人监听的地址：Alive 会很快失败并回 false，
		// 这条测试不关心它的答案，只关心谁在往流上写。
		CaddyAdmin: "http://127.0.0.1:1",
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// caddyJSON 为空 → 没有内联证书 → 立刻报一份空清单。
			a.reportCerts(ctx, w, nil)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.handleProbe(ctx, w, &edgev1.Probe{Id: "p"})
		}()
	}
	wg.Wait()

	maxInFlight, total := o.peak()
	if total != 24 {
		t.Fatalf("流上只到了 %d 条消息，想要 24——装置没把两条路径都驱动起来", total)
	}
	if maxInFlight > 1 {
		t.Errorf("证书回执与探活回执在同一条流上重叠了 %d 个 Send", maxInFlight)
	}
}
