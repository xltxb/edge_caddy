package agent_test

import (
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/agent"
)

// 连续快速失败：指数翻倍，封顶 30s——这半边是原有行为，锁住别丢。
func TestBackoffDoublesOnQuickFailuresAndCaps(t *testing.T) {
	var b agent.ReconnectBackoff
	want := []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	for i, w := range want {
		if got := b.Next(0); got != w {
			t.Fatalf("第 %d 次快速失败后应当等 %v，实际 %v", i+1, w, got)
		}
	}
}

// 一条连接活得够久，说明上一次重连是成功的——下一次断开从 1s 重新开始。
//
// 不复位的话（真实日志里看到的 after=1s→2s→4s，三次断开相隔一到几小时），
// 几天之后每次本来一秒就能恢复的重连都要白等 30 秒。
func TestBackoffResetsAfterSteadyConnection(t *testing.T) {
	var b agent.ReconnectBackoff
	b.Next(0)
	b.Next(0) // 攒到 4s 档
	if got := b.Next(2 * time.Hour); got != 1*time.Second {
		t.Fatalf("稳定跑了两小时之后断开，应当从 1s 重来，实际 %v", got)
	}
	if got := b.Next(0); got != 2*time.Second {
		t.Fatalf("复位之后再快速失败，应当回到翻倍序列的 2s，实际 %v", got)
	}
}

// 「够久」的线是 1 分钟：连上几十秒就断，仍算失败循环，继续退。
// 这条线挡住的是「连上→立刻被踢」的循环把退避永远钉在 1s。
func TestBackoffTreatsShortLivedConnectionAsFailure(t *testing.T) {
	var b agent.ReconnectBackoff
	b.Next(0)
	if got := b.Next(59 * time.Second); got != 2*time.Second {
		t.Fatalf("只活了 59s 不该复位，应当继续 2s，实际 %v", got)
	}
	if got := b.Next(time.Minute); got != 1*time.Second {
		t.Fatalf("活满 1 分钟应当复位到 1s，实际 %v", got)
	}
}
