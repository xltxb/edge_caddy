package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// 只接受同源请求。控制台与接口同源部署（前端文档 §7），跨源的 WS 请求
	// 没有正当来源——放开它等于给 CSWSH（跨站 WebSocket 劫持）开门：
	// 浏览器会带上会话 Cookie，而 WS 握手不受同源策略保护。
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // 非浏览器客户端（如 curl）没有 Origin
		}
		return sameHost(origin, r.Host)
	},
}

func sameHost(origin, host string) bool {
	for _, p := range []string{"http://", "https://"} {
		if len(origin) > len(p) && origin[:len(p)] == p {
			return origin[len(p):] == host
		}
	}
	return false
}

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 30 * time.Second
)

// serveWS 把一个 HTTP 连接升级为实时通道。
func (h *handler) serveWS(c *gin.Context) {
	if h.deps.Hub == nil {
		fail(c, http.StatusServiceUnavailable, codeInternal, "实时通道未装配")
		return
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade 失败时它已经写过响应了，这里只记日志
		h.log.Warn("WebSocket 升级失败", "err", err, "origin", c.GetHeader("Origin"))
		return
	}
	defer conn.Close()

	ch := h.deps.Hub.Subscribe()
	defer h.deps.Hub.Unsubscribe(ch)

	// 读协程只负责收 pong 与感知断开。不读的话，对端关闭我们不会及时知道，
	// 订阅会一直挂着占缓冲。
	go func() {
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(pongWait))
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				_ = conn.Close()
				return
			}
		}
	}()

	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case frame, open := <-ch:
			if !open {
				return
			}
			blob, err := json.Marshal(frame)
			if err != nil {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, blob); err != nil {
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
