package api_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/health"
	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/testdb"
)

// timingHealth 只记下 SetTiming 收到了什么，其余方法是空壳。
type timingHealth struct {
	mu        sync.Mutex
	interval  time.Duration
	threshold int
	calls     int
}

func (h *timingHealth) CPUSeries(string) []int                { return nil }
func (h *timingHealth) Latest(string) (health.Sample, bool)   { return health.Sample{}, false }
func (h *timingHealth) StaleAfter() time.Duration             { return time.Minute }
func (h *timingHealth) Forget(string)                         {}
func (h *timingHealth) BlockedLastHour(string) (uint64, bool) { return 0, false }
func (h *timingHealth) SetTiming(iv time.Duration, thr int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.interval, h.threshold = iv, thr
	h.calls++
}

// TestPutSettingsAppliesTimingImmediately：改间隔 / 阈值不必重启主控。
//
// 此前 PUT 只写库，跑着的离线判定照旧用启动时读到的值，
// 而响应说的是「已保存」——人以为改了，灰度上就照旧被 CDN 断线摘解析。
func TestPutSettingsAppliesTimingImmediately(t *testing.T) {
	st := testdb.New(t)
	if err := st.CreateUser(context.Background(), "abiu", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	sealer, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	h := &timingHealth{}
	r := api.New(api.Options{Store: st, SessionTTL: time.Hour, Sealer: sealer, Health: h})
	ck := login(t, r)

	_, env := do(t, r, "PUT", "/api/v1/settings",
		map[string]any{"offline_threshold_count": 7},
		func(req *http.Request) { req.AddCookie(ck) })
	if env.Code != 0 {
		t.Fatalf("PUT /settings code=%d msg=%s", env.Code, env.Msg)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.calls != 1 {
		t.Fatalf("SetTiming 被调了 %d 次，想要 1 次", h.calls)
	}
	// 只改阈值时，间隔要带上库里那个，而不是零值。
	if h.interval != 3*time.Second || h.threshold != 7 {
		t.Fatalf("SetTiming(%v, %d)，想要 (3s, 7)", h.interval, h.threshold)
	}
}
