package tunnel

import (
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xltxb/edge_caddy/internal/wsconn"
)

// chanListener 是一个由别处「喂」进来的 net.Listener。
//
// gRPC 只认 net.Listener，而 WebSocket 连接是 HTTP handler 里冒出来的。
// 这个 listener 就是那道接缝：handler 把连接推进来，gRPC 从这儿 Accept。
type chanListener struct {
	ch     chan net.Conn
	addr   net.Addr
	closed chan struct{}
	once   sync.Once
}

type wsAddr struct{}

func (wsAddr) Network() string { return "websocket" }
func (wsAddr) String() string  { return "wss://.../api/v1/tunnel" }

func newChanListener() *chanListener {
	return &chanListener{ch: make(chan net.Conn), addr: wsAddr{}, closed: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }

// push 把一条连接交给 gRPC，并**等它被接走**。
//
// 不等的话，没人接的那条连接就留在原地：Upgrade 已经把它 Hijack 走了，
// net/http 不再管它，而这里又把它丢了——**一条谁也不拥有的连接**。
// 它不会报错，只是永远不被读，直到 TCP 超时。节点那一侧看到的是
// 「连上了、然后握手卡住」。
//
// 正常情况下 gRPC 的 Accept 一直在等，这个 select 立刻就走通了。
// 会真的排队的是启动那一瞬和一批节点同时重连的时候。
//
// **探针说明**：把 `push` 改成无条件丢弃，节点就再也上不了线（验过）；
// 而只加一个 `default:` 分支不会红——因为接收方总是就绪，那个分支走不到。
// 后者不是「这段代码没用」，是**那次破坏没进到要验的那条路上**。
func (l *chanListener) push(c net.Conn) error {
	select {
	case l.ch <- c:
		return nil
	case <-l.closed:
		return net.ErrClosed
	case <-time.After(10 * time.Second):
		return errors.New("gRPC 没有来取这条连接")
	}
}

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	ReadBufferSize:   32 * 1024,
	WriteBufferSize:  32 * 1024,
	// **不检查 Origin。** 这个端点的调用方是 Agent，不是浏览器；
	// 它没有 Origin 头，也不受同源策略约束。真正的认证在里层那次
	// mTLS 握手里（ADR-0009），Origin 在这儿一点保护也提供不了——
	// 留一个假的检查比不检查更糟，它会让人以为这里有一道门。
	CheckOrigin: func(*http.Request) bool { return true },
}

// HTTPHandler 把隧道挂到 HTTP 面上，让它能穿过只转发 443 的中间设施。
//
// **里层跑的是原来那套 mTLS + gRPC，一个字节都没改。**
// 外层这条 WebSocket 只是一根管子：节点身份仍然是内部 CA 签发的
// 客户端证书，CA pin 仍然堵着 TOFU，而中间那层 CDN 只看得见外层 TLS。
//
// 这一点是承重的：下发走内联证书（ADR-0010），**每个客户域名的私钥
// 都在这条隧道里传**。让 CDN 看见它们和让它看不见，是两种系统。
func (s *Server) HTTPHandler() http.HandlerFunc {
	s.wsOnce.Do(func() {
		s.wslis = newChanListener()
		go func() {
			// gRPC 允许在多个 listener 上同时 Serve，所以这条与 :9000
			// 那条并存——旧节点不受影响。
			if err := s.grpc.Serve(s.wslis); err != nil && !errors.Is(err, net.ErrClosed) {
				s.log.Error("WebSocket 隧道退出", "err", err)
			}
		}()
	})

	return func(w http.ResponseWriter, r *http.Request) {
		// **不是升级请求就按契约回一个信封。**
		//
		// gorilla 的 Upgrade 在这种情况下写的是纯文本 `Bad Request`，
		// 而 `/api/` 下的一切都该是 `{code,data,msg}`（契约 §0.2）。
		// 更要紧的是那句话说不出问题：一个把 `wss://` 写成 `https://`
		// 的人拿到「Bad Request」，会去查请求体、查参数——
		// 而这个端点根本没有请求体。
		if !websocket.IsWebSocketUpgrade(r) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":1001,"data":null,` +
				`"msg":"这个端点只接受 WebSocket 升级 —— 节点隧道走 wss://，不是 https://"}`))
			return
		}

		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// Upgrade 自己已经写过响应了，这里只记一笔。
			s.log.Warn("隧道升级失败", "err", err, "remote", r.RemoteAddr)
			return
		}
		// **不设读写超时。** 隧道是长连接，心跳由里层的 gRPC 管；
		// 在这一层加超时会在一次安静的间隙里把它掐掉，
		// 而那看起来像网络抖动。
		if err := s.wslis.push(wsconn.New(ws)); err != nil {
			s.log.Warn("隧道连接没能交给 gRPC", "err", err)
			_ = ws.Close()
		}
		// **这里就该返回。** Upgrade 已经把底层连接 Hijack 走了，
		// net/http 不再管它；连接的主人现在是 gRPC，由它负责关闭。
		//
		// 与 internal/ws 那个控制台端点不同：那边 handler 自己跑读写循环，
		// 所以生命周期一致；这边交出去之后就没事做了，
		// 挂在这儿只会为每条隧道留一个什么也不干的 goroutine。
	}
}
