package ws

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 25 * time.Second
)

// Handler 把一个已通过鉴权的 HTTP 请求升级成 WS 连接并泵送帧。
//
// 鉴权在这之前由 api 包的 Auth 中间件完成（契约 §0.6：WS 复用会话 Cookie，
// 未登录直接 401，不升级）。因此这里的 CheckOrigin 只需要挡住跨站升级——
// 同源部署下 Origin 必须与 Host 一致。
func Handler(h *Hub, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	up := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // 非浏览器客户端（e2e、curl）不带 Origin
			}
			return sameHost(origin, r.Host)
		},
	}

	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			log.Warn("ws 升级失败", "err", err)
			return
		}
		defer conn.Close()

		ch, stop := h.Subscribe()
		defer stop()

		// 读循环只负责处理 pong 与感知对端关闭。客户端不发业务帧（契约 §2），
		// 收到任何内容都直接丢弃。
		go func() {
			defer stop()
			conn.SetReadLimit(512)
			_ = conn.SetReadDeadline(time.Now().Add(pongWait))
			conn.SetPongHandler(func(string) error {
				return conn.SetReadDeadline(time.Now().Add(pongWait))
			})
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()

		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()

		for {
			select {
			case b, ok := <-ch:
				if !ok {
					// hub 主动断开（积压超限或关停）。给对端一个正常的关闭帧，
					// 让前端把它当作可重连的断线而不是异常。
					_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
					_ = conn.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseGoingAway, ""))
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
					return
				}
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}
}

func sameHost(origin, host string) bool {
	for _, p := range []string{"http://", "https://"} {
		if len(origin) > len(p) && origin[:len(p)] == p {
			return origin[len(p):] == host
		}
	}
	return false
}
