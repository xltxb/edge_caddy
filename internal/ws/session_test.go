package ws_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// TestConnectionClosesWhenTheSessionGoesAway：会话没了，这条连接也要断。
//
// 鉴权原先只发生在**升级那一刻**（契约 §0.6）。之后登出、管理员删会话、
// 或者 TTL 到期，那条连接仍然活着：节点状态、事件流、下发进度继续送到那个
// 浏览器，直到它自己关掉（issue #64）。
//
// ADR-0013 说控制台访问 = 网络 + 会话。会话那条腿在 WS 上原先只站了一瞬间。
//
// **判据是服务端真的把连接关了**，不是「复核函数被调用过」——后者在一个
// 调了却不处置返回值的实现下同样为真。
func TestConnectionClosesWhenTheSessionGoesAway(t *testing.T) {
	hub := ws.NewHub(nil)
	var valid atomic.Bool
	valid.Store(true)

	srv := httptest.NewServer(ws.Handler(hub, nil, ws.Options{
		// 周期做成参数的理由与 deploy.RetryBackoff 一样：真等 25 秒的测试没人跑。
		PingPeriod: 5 * time.Millisecond,
		StillValid: func(*http.Request) bool { return valid.Load() },
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("连不上: %v", err)
	}
	defer conn.Close()

	// **一个后台读**，不是两次带超时的读：gorilla 的连接在一次读超时之后
	// 就不能再读了，那会把第二阶段要观察的东西毁掉（这条是撞出来的）。
	closed := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				closed <- err
				return
			}
		}
	}()

	// 会话还在时它不该被踢。给它足够多个 ping 周期去犯错。
	select {
	case err := <-closed:
		t.Fatalf("会话有效时连接不该断，实际 err=%v", err)
	case <-time.After(80 * time.Millisecond):
	}

	// 人登出了。
	valid.Store(false)

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("会话没了，连接却还活着 —— 节点状态与事件流会继续送到那个浏览器")
	}
}
