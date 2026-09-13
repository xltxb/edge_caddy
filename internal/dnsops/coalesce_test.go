package dnsops_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/dnsops"
	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
)

// TestConcurrentDetachesCollapseIntoOneSync：同一批离线只推一次解析。
//
// Detach / Attach 把 nodeID 原样丢掉、各自触发一次**全量**同步
// （`return o.Sync(ctx, nil)`）。6 台节点在同一个 tick 里被判离线 → markDown
// 串行调 6 次 Detach → 在 o.mu 下串行推 6 次**内容完全相同**的全量同步
// （issue #55）。恢复时 recover 是 go 起的，6 个 goroutine 排队再推 6 次。
//
// 服务商侧既没有退避也没有调用上限。叠加空轮换护栏（#36）之前，全体掉线时
// 这是 6 次「把记录删光」。
//
// **判据是服务商收到了几次同步**，不是「有没有同步」——后者在 6 次和 1 次
// 之间完全分不出来，而那正是这条 issue 的全部内容。
func TestConcurrentDetachesCollapseIntoOneSync(t *testing.T) {
	var syncs atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Record.List" {
			syncs.Add(1)
			// 慢一点：真服务商不会瞬间答完，而合并窗口正是靠这段时间才有意义。
			time.Sleep(20 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":{"code":"1"},"records":[]}`))
	}))
	defer api.Close()

	st := testdb.New(t)
	ctx := context.Background()
	sealer, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutDNSProvider(ctx, store.DNSProviderSettings{
		Kind: "dnspod", Domain: "example.com", SubName: "cdn", Credential: "12345,tok",
	}, sealer); err != nil {
		t.Fatal(err)
	}

	// 得有节点真在轮换里，否则空轮换护栏（#36）会在列记录之前就短路，
	// 而这条测试数的正是列记录那一步。
	weights := store.DNSWeights{}
	for _, line := range []string{"ct", "cu", "cm", "tw", "ov"} {
		weights[line] = map[string]int{}
	}
	for i := 0; i < 6; i++ {
		id := "node-" + string(rune('a'+i))
		if err := st.UpsertNode(ctx, store.NodeSpec{
			NodeID: id, City: "香港", Vendor: "v", Line: "l",
			PublicIP: "203.0.113." + string(rune('1'+i)),
		}); err != nil {
			t.Fatal(err)
		}
		for line := range weights {
			weights[line][id] = 50
		}
	}
	if err := st.PutDNSWeights(ctx, weights); err != nil {
		t.Fatal(err)
	}

	o := &dnsops.Orchestrator{Store: st, Sealer: sealer, BaseOverride: api.URL}

	// 一次机房故障：六台一起掉。
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = o.Detach(ctx, "node-"+string(rune('a'+n)))
		}(i)
	}
	wg.Wait()

	// 一次在跑 + 最多一次补跑（那一次读的是最新的库状态）。
	if got := syncs.Load(); got > 2 {
		t.Errorf("六台一起掉，推了 %d 次全量同步 —— 内容完全相同，"+
			"而服务商侧既没有退避也没有调用上限", got)
	}
	if syncs.Load() == 0 {
		t.Error("一次都没推 —— 那是另一个方向的错")
	}
}

// panicOnError 在收到一条 Error 日志时炸。
//
// 它替的是 syncLocked 里那句「记录解析同步结果失败」——生产里那一行下面
// 就是服务商 SDK 与渲染，随便哪一处 panic 都会走到同一个地方。用 logger
// 当注入点，是为了**不在生产结构体上为测试开一个口子**。
type panicOnError struct{ slog.Handler }

func (h panicOnError) Enabled(context.Context, slog.Level) bool { return true }
func (h panicOnError) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		panic("注入：记日志时炸了")
	}
	return nil
}
func (h panicOnError) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h panicOnError) WithGroup(string) slog.Handler      { return h }

// TestPanicDoesNotWedgeTheOrchestrator：一次 panic 不该把解析编排器锁死。
//
// syncCoalesced 原先是手工配对的 `o.mu.Lock()` … `o.mu.Unlock()`，中间夹着
// syncLocked；`close(done.ready)` 同样在最后一行。两者都不是 defer。
//
// 于是 syncLocked 里任何一次 panic 的后果是**进程级**的，而不是「这个请求
// 500」：gin 的 Recovery 会把那次请求救回来，控制台看起来一切正常，而
//
//   - o.mu 永不释放 —— 此后每一次解析同步（人工改权重、节点开关、心跳摘挂、
//     自愈）永久阻塞，没有超时、没有报错，就是不返回；
//   - 排在 done 上的调用方连 500 都等不到，它们挂在 <-done.ready 上。
//
// **判据是「后面的人还走得动」**，不是「panic 有没有发生」——后者两种实现
// 都一样。
func TestPanicDoesNotWedgeTheOrchestrator(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":{"code":"1"},"records":[]}`))
	}))
	defer api.Close()

	st := testdb.New(t)
	ctx := context.Background()
	sealer, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutDNSProvider(ctx, store.DNSProviderSettings{
		Kind: "dnspod", Domain: "example.com", SubName: "cdn", Credential: "12345,tok",
	}, sealer); err != nil {
		t.Fatal(err)
	}

	o := &dnsops.Orchestrator{
		Store: st, Sealer: sealer, BaseOverride: api.URL,
		Log: slog.New(panicOnError{}),
	}

	// 让 PutDNSSync 失败 —— 那一句失败会走 Error 日志，而这里的 handler 会炸。
	// 这是 syncLocked 里**无论成败都会走到**的一步。
	st.Pool.Close()

	panicked := make(chan struct{})
	go func() {
		defer func() {
			if recover() != nil {
				close(panicked)
			}
		}()
		_ = o.Detach(ctx, "node-a")
	}()
	select {
	case <-panicked:
	case <-time.After(3 * time.Second):
		t.Fatal("装置坏了：注入的 panic 没炸起来")
	}

	// 编排器还得能用。这里不在乎它成不成功——池已经关了，它必然失败——
	// 只在乎**它会返回**。
	done := make(chan struct{})
	go func() {
		defer func() { _ = recover(); close(done) }()
		_ = o.Attach(ctx, "node-a")
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("一次 panic 之后解析同步再也不返回了 —— o.mu 没人放，" +
			"此后每一次改权重、点开关、心跳摘挂都会永久阻塞在这里")
	}
}
