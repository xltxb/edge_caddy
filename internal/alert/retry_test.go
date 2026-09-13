package alert

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestOnlyRetryWhatTimeCanFix：只重试时间能修好的那些。
//
// 投递原先对任何非 2xx 都重试三次，于是一个配错的 Webhook（404 / 401 / 400）
// 每条告警都打三遍（issue #56）。
//
// **要挑明**：ADR-0005 的字面范围是 Caddy 配置下发（「同一份字节喂给同一个
// Caddy 必然得到同一个拒绝，能修它的是人改配置，不是时间」），不覆盖 Webhook
// 投递，所以这不算硬违反。但 404 / 401 / 400 的性质与它论证的完全相同：
// 地址写错了、token 过期了——重试三次只是把同一句拒绝再听两遍。
//
// 5xx 与 429 是另一回事：那是「此刻不行」，时间确实修得好。
func TestOnlyRetryWhatTimeCanFix(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   int32
	}{
		{"404 地址写错了，重试没有意义", http.StatusNotFound, 1},
		{"401 凭证不对，重试没有意义", http.StatusUnauthorized, 1},
		{"400 载荷被拒，重试没有意义", http.StatusBadRequest, 1},
		{"500 下游此刻不行，时间修得好", http.StatusInternalServerError, 3},
		{"429 被限流了，时间修得好", http.StatusTooManyRequests, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(c.status)
			}))
			defer srv.Close()

			n := &Notifier{HTTP: srv.Client(), retryBase: time.Millisecond}
			if err := n.postWithRetry(context.Background(), srv.URL, []byte(`{}`), 3); err == nil {
				t.Fatal("下游一直拒绝，应当回错")
			}
			if got := hits.Load(); got != c.want {
				t.Errorf("打了 %d 次，想要 %d", got, c.want)
			}
		})
	}
}

// TestTransportFailureStillRetries：连不上仍然重试。
//
// 这是 ADR-0005 那条判据的正面：连不上是传输层失败，而传输层失败正是时间
// 可能修好的那一类。少了这条，一个「什么都不重试」的实现也能让上面全绿。
func TestTransportFailureStillRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // 关掉：此后连不上

	var dials atomic.Int32
	n := &Notifier{
		retryBase: time.Millisecond,
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			dials.Add(1)
			return nil, http.ErrHandlerTimeout
		})},
	}
	if err := n.postWithRetry(context.Background(), url, []byte(`{}`), 3); err == nil {
		t.Fatal("一直连不上，应当回错")
	}
	if got := dials.Load(); got != 3 {
		t.Errorf("试了 %d 次，想要 3——传输层失败是时间修得好的那一类", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
