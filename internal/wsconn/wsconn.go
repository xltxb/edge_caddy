// Package wsconn 把一条 WebSocket 变成一个普通的 net.Conn。
//
// **它存在的理由只有一个：让隧道穿过只转发 80/443 的中间设施。**
//
// 灰度上撞到的：主控的域名挂在 Cloudflare 后面，而 CDN 只代理 80/443，
// 隧道那个 9000 端口的包根本到不了主控。节点装完一切正常，
// 然后**永远不出现在控制台里**。
//
// 换成 WebSocket 之后，隧道走的是 `wss://<主控域名>/api/v1/tunnel`——
// 一条在任何 CDN、任何企业代理眼里都平平无奇的 443 连接。
//
// # 为什么不顺手把节点身份也改成应用层凭证
//
// 因为不需要，而且那样会拆掉一层真实的保护。
//
// `crypto/tls` 和 gRPC 都只要一个 `net.Conn`。所以**原来那套 mTLS 握手
// 原封不动地在这条 WebSocket 里面跑**：节点身份仍然是内部 CA 签发的
// 客户端证书（ADR-0009），`identify()` 一行不改，CA pin 照旧堵住 TOFU。
//
// 代价是 TLS-in-TLS 两层加密。对一条心跳几秒一次、偶尔推一次配置的
// 控制面连接，这点开销可以忽略；而换来的是：
//
//	**中间那层 CDN 只看得见外层 TLS，隧道里的东西对它是不透明的。**
//
// 这一条不是洁癖：下发走的是内联证书（ADR-0010），
// **每一个客户域名的私钥都在这条隧道里传**。
package wsconn

import (
	"io"
	"net"
	"time"

	"github.com/gorilla/websocket"
)

// Conn 是一条 WebSocket 之上的 net.Conn。
//
// gorilla 的并发约定是「同时最多一个读者、一个写者」，而这正好是
// crypto/tls 的用法（读循环与写循环各一条），所以这里不需要额外的锁。
// **加锁反而会死锁**：TLS 在握手期间会在同一个 goroutine 里读写交替。
type Conn struct {
	ws *websocket.Conn
	// r 是当前这一帧还没读完的部分。**必须有它。**
	// WebSocket 是消息流，net.Conn 是字节流：一帧 16KB 而调用方给了
	// 一个 512 字节的缓冲区是常态（TLS 记录层就是这么读的），
	// 丢掉剩下的部分会让连接静默错位——表现是握手失败，
	// 而错误信息指向证书。
	r io.Reader
}

func New(ws *websocket.Conn) *Conn { return &Conn{ws: ws} }

func (c *Conn) Read(p []byte) (int, error) {
	for {
		if c.r != nil {
			n, err := c.r.Read(p)
			if err == io.EOF {
				// 这一帧读完了，换下一帧。**EOF 不能往上抛**：
				// 它在 net.Conn 的语义里意味着连接结束，
				// 而这里只是一条消息结束。
				c.r = nil
				if n > 0 {
					return n, nil
				}
				continue
			}
			return n, err
		}
		_, r, err := c.ws.NextReader()
		if err != nil {
			return 0, translate(err)
		}
		c.r = r
	}
}

func (c *Conn) Write(p []byte) (int, error) {
	// 用 BinaryMessage：TextMessage 要求 UTF-8，而这里传的是 TLS 记录。
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, translate(err)
	}
	return len(p), nil
}

func (c *Conn) Close() error         { return c.ws.Close() }
func (c *Conn) LocalAddr() net.Addr  { return c.ws.LocalAddr() }
func (c *Conn) RemoteAddr() net.Addr { return c.ws.RemoteAddr() }
func (c *Conn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}
func (c *Conn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }

// translate 把 WebSocket 的正常关闭翻成 io.EOF。
//
// 不翻的话，一次**正常**的对端关闭会以 `websocket: close 1000 (normal)`
// 的形式冒到 TLS 和 gRPC 那里，被当成传输错误记进日志——
// 于是每一次节点正常重连都会在主控上留下一条像是故障的记录。
func translate(err error) error {
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return io.EOF
	}
	return err
}
