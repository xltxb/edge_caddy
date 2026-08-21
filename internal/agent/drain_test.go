package agent

import (
	"context"
	"testing"
	"time"
)

// 连接立刻就是 0：排空成功，不该白等一个轮询周期。
func TestWaitDrainedReturnsImmediatelyWhenNoConns(t *testing.T) {
	start := time.Now()
	drained, remaining := waitDrained(context.Background(),
		func(context.Context) uint32 { return 0 },
		time.Second, 200*time.Millisecond)

	if !drained || remaining != 0 {
		t.Fatalf("drained=%v remaining=%d，想要 true/0", drained, remaining)
	}
	if el := time.Since(start); el > 100*time.Millisecond {
		t.Errorf("没有连接时不该等：耗时 %v", el)
	}
}

// **到点了还有连接，要如实报剩下多少。**
//
// 这里最容易写成回一个布尔就完事。而人接下来要做的决定是「现在能不能关机」——
// 一个 drained=false 答不了那个问题：是还剩 2 条可以直接关，
// 还是还剩 8000 条得再等。
func TestWaitDrainedReportsRemainingOnTimeout(t *testing.T) {
	drained, remaining := waitDrained(context.Background(),
		func(context.Context) uint32 { return 7 },
		300*time.Millisecond, 50*time.Millisecond)

	if drained {
		t.Error("连接一直在，不该报排空成功")
	}
	if remaining != 7 {
		t.Errorf("remaining = %d，想要 7", remaining)
	}
}

// 连接逐渐降到 0：要等到那一刻，而不是看第一眼不为 0 就放弃。
func TestWaitDrainedWaitsForConnsToFall(t *testing.T) {
	var calls int
	drained, remaining := waitDrained(context.Background(),
		func(context.Context) uint32 {
			calls++
			if calls >= 3 {
				return 0
			}
			return uint32(5 - calls)
		},
		2*time.Second, 20*time.Millisecond)

	if !drained || remaining != 0 {
		t.Fatalf("drained=%v remaining=%d，想要 true/0", drained, remaining)
	}
	if calls < 3 {
		t.Errorf("应当反复查到降为 0，实际只查了 %d 次", calls)
	}
}

// 隧道断了或进程要退了：报的数字仍然是真的，只是没等到 0。
func TestWaitDrainedStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(60 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	drained, remaining := waitDrained(ctx,
		func(context.Context) uint32 { return 4 },
		10*time.Second, 20*time.Millisecond)

	if drained {
		t.Error("被打断时不该报排空成功")
	}
	if remaining != 4 {
		t.Errorf("remaining = %d，想要 4", remaining)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("被打断后应当立刻返回，耗时 %v", el)
	}
}

// 超时为 0 时用默认值，不是「立刻超时」。
//
// 主控不给 timeout_ms 的话，一个「0 = 马上放弃」的实现会让排空静默地
// 什么也不做，而结果看起来完全正常：drained=false、remaining 是真的数字。
func TestWaitDrainedTreatsZeroTimeoutAsDefault(t *testing.T) {
	var calls int
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	waitDrained(ctx, func(context.Context) uint32 { calls++; return 3 },
		0, 20*time.Millisecond)

	if calls < 2 {
		t.Errorf("timeout=0 应当走默认值继续等，实际只查了 %d 次", calls)
	}
}
