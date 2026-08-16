package enroll_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/enroll"
	"github.com/xltxb/edge_caddy/internal/store"
)

func newEnroller(t *testing.T) (*enroll.Enroller, context.Context) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "e.sqlite"))
	if err != nil {
		t.Fatalf("打开存储: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return enroll.New(st), context.Background()
}

// 一次性：同一个 Token 消费第二次必须失败。
//
// 这是接入凭据的核心性质。校验通过却不作废，等于把它变成了 TTL 窗口内可无限
// 复用的凭据——任何拿到过它的人都能再接入一台机器，并让主控为其签发隧道证书。
func TestTokenIsSingleUse(t *testing.T) {
	e, ctx := newEnroller(t)

	tok, _, err := e.Issue(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("签发: %v", err)
	}
	if err := e.Consume(ctx, tok, "node-hk-01"); err != nil {
		t.Fatalf("首次消费应成功: %v", err)
	}
	if err := e.Consume(ctx, tok, "node-hk-02"); err == nil {
		t.Fatal("同一 Token 不应被消费第二次")
	}
}

// 消费必须跨主控重启仍然有效。
//
// 若「用过了」只记在内存里，主控重启后同一个 Token 又能再用一次——而重启在
// 30 分钟的 TTL 窗口内完全可能发生（一次发版就够了）。
func TestConsumptionSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e.sqlite")
	ctx := context.Background()

	st1, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := enroll.New(st1).Issue(ctx, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := enroll.New(st1).Consume(ctx, tok, "node-hk-01"); err != nil {
		t.Fatalf("首次消费应成功: %v", err)
	}
	_ = st1.Close()

	// 模拟主控重启：同一个库重新打开，全新的 Enroller
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if err := enroll.New(st2).Consume(ctx, tok, "node-evil"); err == nil {
		t.Fatal("重启后同一 Token 仍不应可用")
	}
}

func TestUnknownTokenIsRejected(t *testing.T) {
	e, ctx := newEnroller(t)
	if err := e.Consume(ctx, "enr_deadbeef", "node-hk-01"); err == nil {
		t.Fatal("未签发过的 Token 应被拒绝")
	}
	if err := e.Consume(ctx, "", "node-hk-01"); err == nil {
		t.Fatal("空 Token 应被拒绝")
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	e, ctx := newEnroller(t)
	tok, _, err := e.Issue(ctx, -time.Second) // 已经过期
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Consume(ctx, tok, "node-hk-01"); err == nil {
		t.Fatal("过期 Token 应被拒绝")
	}
}

// 两台机器同时拿同一个 Token 抢接入时，只能有一个成功。
//
// 这不是理论情况：安装命令是复制粘贴的，粘到两台机器上并同时执行很常见。
// 若两个都成功，两台机器会以同一个身份接入，主控无法区分它们。
func TestConcurrentConsumeAllowsExactlyOne(t *testing.T) {
	e, ctx := newEnroller(t)
	tok, _, err := e.Issue(ctx, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	const racers = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		accepted int
	)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			if err := e.Consume(ctx, tok, "node-race"); err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if accepted != 1 {
		t.Fatalf("并发消费应恰好成功 1 次，实际 %d 次", accepted)
	}
}
