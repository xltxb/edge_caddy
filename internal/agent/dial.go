package agent

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xltxb/edge_caddy/internal/wsconn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// tunnelPath 是隧道挂在 HTTP 面上的路径（契约 §2）。
const tunnelPath = "/api/v1/tunnel"

// dialOptions 把「主控地址」翻成一组 gRPC 拨号选项。
//
// 支持两种写法，**而它们的区别是承重的，不是风格问题**：
//
//	ec.example.com:9000              直连 gRPC。端口是自己的，中间不能有 CDN。
//	wss://cdn.example.com            隧道走 WebSocket，穿 443。
//
// 后者是为了穿过只转发 80/443 的中间设施。灰度上撞到的：主控域名挂在
// Cloudflare 后面，CDN 不代理 9000，节点装完一切正常，
// 然后**永远不出现在控制台里**——那台机器上没有任何东西说得出为什么。
//
// **两种写法下里层完全一样**：同一套 mTLS、同一个内部 CA、同一个 CA pin、
// 同一份 gRPC。WebSocket 只是一根管子，中间那层 CDN 看得见的只有外层 TLS。
// 这一点是承重的：下发走内联证书（ADR-0010），
// **每个客户域名的私钥都在这条隧道里传**。
func dialOptions(master string, creds credentials.TransportCredentials) (string, []grpc.DialOption, error) {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		// 与主控那一侧对齐（internal/tunnel 的 maxTunnelMsgBytes）。
		// **只调一侧的话，超限的那一端会在发送时就失败，
		// 而另一端连一条日志都不会有。**
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(32<<20),
			grpc.MaxCallSendMsgSize(32<<20),
		),
	}

	if !strings.Contains(master, "://") {
		// 直连：保持原样，旧节点的配置一个字都不用改。
		return master, opts, nil
	}

	u, err := url.Parse(master)
	if err != nil {
		return "", nil, fmt.Errorf("主控地址 %q 不是合法 URL: %w", master, err)
	}
	if u.Scheme != "wss" && u.Scheme != "ws" {
		return "", nil, fmt.Errorf("主控地址 %q 的协议是 %q —— 只支持 wss://（或本地调试用 ws://），"+
			"或者写成 host:port 直连 gRPC", master, u.Scheme)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = tunnelPath
	}
	target := u.Host
	if u.Port() == "" {
		// **补出默认端口，不要让它留空。** gRPC 的 authority 会成为里层 TLS 的
		// ServerName，而主控证书的 SAN 里是主机名——留空时两边对 authority 的
		// 拼法不一致，症状是握手报「证书不适用于该主机名」，
		// 而人会去查证书，那儿没有问题。
		if u.Scheme == "wss" {
			target = net.JoinHostPort(u.Hostname(), "443")
		} else {
			target = net.JoinHostPort(u.Hostname(), "80")
		}
	}

	wsURL := *u
	dialer := &websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		ReadBufferSize:   32 * 1024,
		WriteBufferSize:  32 * 1024,
		// **外层 TLS 走系统信任库，不是内部 CA。**
		//
		// 外层要让 CDN / 反代认得出来，所以它必须是一张公网证书；
		// 而节点的身份、主控的身份**都在里层那次握手里定**（ADR-0009）。
		// 外层被谁终止都不影响里层——这正是这个设计成立的原因。
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}

	opts = append(opts, grpc.WithContextDialer(
		func(ctx context.Context, _ string) (net.Conn, error) {
			ws, resp, err := dialer.DialContext(ctx, wsURL.String(),
				http.Header{"User-Agent": []string{"edge-agent"}})
			if err != nil {
				// **把 HTTP 状态码带出来。** WebSocket 握手失败时
				// gorilla 只给一句 `bad handshake`，而真正的信息在响应里：
				// 404 = 主控版本太老（没有这个端点）或路径写错，
				// 502 = 反代没转到主控，401 = 这个端点被鉴权挡住了。
				// 少了状态码，这三种会长成同一句话。
				if resp != nil {
					return nil, fmt.Errorf("连接隧道 %s 失败（HTTP %d）: %w",
						wsURL.String(), resp.StatusCode, err)
				}
				return nil, fmt.Errorf("连接隧道 %s 失败: %w", wsURL.String(), err)
			}
			return wsconn.New(ws), nil
		}))

	// passthrough：地址已经定死了，不要让 gRPC 再去做一次名字解析。
	return "passthrough:///" + target, opts, nil
}
