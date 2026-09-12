package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
)

// fakeStream 是一条能被测试掐断的隧道。
//
// Recv 一直挡着，直到 breakIt 被调用——那一刻它回一个错误，与真隧道断开时
// 一模一样。Send 只数数：这条测试关心的是**还有没有人在发**，不是发了什么。
type fakeStream struct {
	broken chan struct{}
	once   sync.Once

	mu    sync.Mutex
	sends int
}

func newFakeStream() *fakeStream { return &fakeStream{broken: make(chan struct{})} }

func (f *fakeStream) Send(*edgev1.AgentMsg) error {
	f.mu.Lock()
	f.sends++
	f.mu.Unlock()
	return nil
}

func (f *fakeStream) Recv() (*edgev1.MasterMsg, error) {
	<-f.broken
	return nil, errors.New("隧道断开")
}

func (f *fakeStream) breakIt() { f.once.Do(func() { close(f.broken) }) }

func (f *fakeStream) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends
}

// TestServeStopsItsLoopsWhenTheTunnelDrops：隧道断开之后，这次连接起的后台
// 循环必须停下来。
//
// 它们原先只在**调用方那个 ctx** 结束时退出，而调用方传的是进程级的信号 ctx
// （cmd/agent/main.go 的重连循环）——隧道断开不会取消它。于是 Run 返回、
// main 重连、再起一份，而上一份还在那儿转（issue #42）。
//
// dial.go:29 记着真实日志：中间设施按小时掐长连接，一天断三次。断 N 次就是
// N 份心跳循环，每份每 3 秒做一次 200ms 的 CPU 采样并列一遍全机 TCP 连接。
//
// **判据是「serve 返回之后流上还有没有新的发送」**，不是 goroutine 计数：
// 计数要配一个容差，而容差正好盖住「泄漏了一两条」这种最常见的情形。
func TestServeStopsItsLoopsWhenTheTunnelDrops(t *testing.T) {
	f := newFakeStream()
	a := New(Config{
		NodeID:     "node-hk-01",
		CaddyAdmin: "http://127.0.0.1:1",
		Heartbeat:  5 * time.Millisecond,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	// 进程级的 ctx：它在整个测试期间都不会结束，正如线上那条。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.serve(ctx, f, newTunnelWriter(f))
	}()

	// 等心跳真的跑起来，否则下面那个「不再增长」会因为它还没开始而假绿。
	deadline := time.Now().Add(2 * time.Second)
	for f.sendCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.sendCount() == 0 {
		t.Fatal("心跳一条都没发出来——装置没跑起来，后面的断言是空的")
	}

	f.breakIt()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("隧道断了，serve 没有返回")
	}

	// serve 已经返回。给还活着的循环足够多个周期去暴露自己。
	settled := f.sendCount()
	time.Sleep(60 * time.Millisecond) // 12 个心跳周期
	if got := f.sendCount(); got != settled {
		t.Errorf("serve 返回后流上又多了 %d 条消息——这次连接的循环没有停，"+
			"而 main 已经在重连并再起一份了", got-settled)
	}
}
