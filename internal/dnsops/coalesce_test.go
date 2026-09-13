package dnsops_test

import (
	"context"
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
