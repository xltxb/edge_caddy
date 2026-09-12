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

// Options 是这条连接的两个可调项。
type Options struct {
	// StillValid 在每个 ping 周期复核一次「这个人现在还登录着吗」。
	//
	// **升级那一刻的鉴权只管那一刻。** 之后登出、管理员删会话、或者 TTL 到期，
	// 连接仍然活着：节点状态、事件流、下发进度继续送到那个浏览器，直到它自己
	// 关掉（issue #64）。ADR-0013 说控制台访问 = 网络 + 会话，而会话那条腿
	// 在 WS 上原先只站了一瞬间。
	//
	// 留空表示不复核——本地跑与单测里不关心会话时可以这样。
	StillValid func(*http.Request) bool

	// PingPeriod 留空即用默认的 25 秒。做成字段只为让复核能被单独测：
	// 真等 25 秒的测试不会有人跑（与 deploy.RetryBackoff 同一条理由）。
	PingPeriod time.Duration
}

// Handler 把一个已通过鉴权的 HTTP 请求升级成 WS 连接并泵送帧。
//
// 升级那一刻的鉴权由 api 包的 Auth 中间件完成（契约 §0.6：WS 复用会话 Cookie，
// 未登录直接 401，不升级）；此后由 opt.StillValid 每个 ping 周期复核一次。
// 这里的 CheckOrigin 只需要挡住跨站升级——同源部署下 Origin 必须与 Host 一致。
func Handler(h *Hub, log *slog.Logger, opt Options) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	period := opt.PingPeriod
	if period <= 0 {
		period = pingPeriod
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

		ticker := time.NewTicker(period)
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
				// **顺着心跳复核会话**，不另起一个定时器：要问的是同一个
				// 「这条连接还该活着吗」，而两个周期迟早会各自漂移。
				if opt.StillValid != nil && !opt.StillValid(r) {
					_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
					// 用正常关闭帧而不是直接掐掉：前端据此跳登录页，
					// 而一个异常断开会被它当成可重连的抖动，然后一直重连下去。
					_ = conn.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, "会话已失效"))
					return
				}
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
