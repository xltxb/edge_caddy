package agent_test

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/caddytest"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/render"
)

// selfSigned 用内部 CA 签一张服务端证书，代替真实的 ACME 签发。
//
// 这里不打真 ACME：那需要一个公网可解析的域名、一个 DNS 服务商账号，
// 而且会在 Let's Encrypt 上留下真实的签发记录。要验的是**下发与加载**
// 这条链路，而它与证书由谁签无关。
func selfSigned(t *testing.T, domain string) render.Cert {
	t.Helper()
	ca, err := pki.GenerateCA(pki.KindTunnel)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.SignServer(domain, []string{domain}, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return render.Cert{Domain: domain, CertPEM: leaf.CertPEM, KeyPEM: leaf.KeyPEM}
}

// ADR-0010 的核心：证书用 load_pem 内联下发，节点上**真的能握手**。
//
// 这条验的不是「配置被接受了」——那太弱。它验的是一个真实的 TLS 客户端
// 连上去之后，拿到的是我们下发的那张证书。
func TestInlinedCertIsActuallyServedOverTLS(t *testing.T) {
	up := upstream(t, "OVER TLS")
	c := caddytest.New(t)
	cert := selfSigned(t, "secure.example.com")

	cfg, issues := render.Render(
		[]model.Route{{Domain: "secure.example.com", Upstream: up, BlockMode: model.BlockAbort}},
		nil, []render.Cert{cert},
		render.Options{HTTPListen: c.EdgeListen(), HTTPSListen: c.TLSListen()})
	if len(issues) > 0 {
		t.Fatalf("渲染报了问题: %v", issues)
	}
	if _, err := agent.NewCaddyClient(c.AdminURL()).ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatalf("Caddy 拒绝了带内联证书的配置: %v", err)
	}

	state := handshakeVia(t, c.TLSSocketPath(), "secure.example.com")
	if len(state.PeerCertificates) == 0 {
		t.Fatal("对端没有出示证书")
	}
	got := state.PeerCertificates[0]
	if got.Subject.CommonName != "secure.example.com" {
		t.Fatalf("出示的证书 CN = %q，想要 secure.example.com", got.Subject.CommonName)
	}
}

// 内联证书下 HTTPS 请求真的能回源。
func TestHTTPSRequestReachesUpstream(t *testing.T) {
	up := upstream(t, "OVER TLS")
	c := caddytest.New(t)
	cert := selfSigned(t, "secure.example.com")

	cfg, _ := render.Render(
		[]model.Route{{Domain: "secure.example.com", Upstream: up, BlockMode: model.BlockAbort}},
		nil, []render.Cert{cert},
		render.Options{HTTPListen: c.EdgeListen(), HTTPSListen: c.TLSListen()})
	if _, err := agent.NewCaddyClient(c.AdminURL()).ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	cli := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				raw, err := (&net.Dialer{}).DialContext(ctx, "unix", c.TLSSocketPath())
				if err != nil {
					return nil, err
				}
				conn := tls.Client(raw, &tls.Config{
					ServerName: "secure.example.com", InsecureSkipVerify: true,
					MinVersion: tls.VersionTLS12,
				})
				return conn, conn.HandshakeContext(ctx)
			},
		},
	}
	resp, err := cli.Get("https://secure.example.com/")
	if err != nil {
		t.Fatalf("HTTPS 请求失败: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "OVER TLS" {
		t.Fatalf("回源结果 = %q", b)
	}
}

// **:80 那台不受影响。**
//
// ADR-0010 实测过：给 server 加 tls_connection_policies 会让整台 server 转 TLS。
// 我们只把它加在 :443 那台上；加错地方会让同节点上所有没有服务端证书的域名
// 立即失联。
func TestPlainHTTPServerStillWorksWhenCertsPresent(t *testing.T) {
	up := upstream(t, "STILL PLAIN")
	c := caddytest.New(t)

	cfg, _ := render.Render([]model.Route{
		{Domain: "secure.example.com", Upstream: up, BlockMode: model.BlockAbort},
		{Domain: "plain.example.com", Upstream: up, BlockMode: model.BlockAbort},
	}, nil, []render.Cert{selfSigned(t, "secure.example.com")},
		render.Options{HTTPListen: c.EdgeListen(), HTTPSListen: c.TLSListen()})
	if _, err := agent.NewCaddyClient(c.AdminURL()).ApplyConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	if code, body := c.Get("plain.example.com", "/", nil); code != 200 || body != "STILL PLAIN" {
		t.Fatalf("明文 server 应当不受影响，得到 %d %q", code, body)
	}
}

// 一张证书都没有时，节点上原有的 apps/tls 内容**原样保留**（ADR-0010）。
//
// 反过来的话，一个还没签发证书的系统会把外部证书平台写入的内容抹掉——
// 那是上一版真出过的事故。
func TestNoCertsLeavesExistingTLSAppUntouched(t *testing.T) {
	up := upstream(t, "X")
	c := caddytest.New(t)
	cli := agent.NewCaddyClient(c.AdminURL())
	ctx := context.Background()

	// 先模拟「节点上已经有别人写的 tls 配置」。
	first, _ := render.Render([]model.Route{{Domain: "a.example.com", Upstream: up, BlockMode: model.BlockAbort}},
		nil, []render.Cert{selfSigned(t, "a.example.com")},
		render.Options{HTTPListen: c.EdgeListen(), HTTPSListen: c.TLSListen()})
	if _, err := cli.ApplyConfig(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Config()["apps"].(map[string]any)["tls"]; !ok {
		t.Fatal("前置条件不成立：应当先有一份 tls 配置")
	}

	// 再下发一份没有证书的配置。
	second, _ := render.Render([]model.Route{{Domain: "a.example.com", Upstream: up, BlockMode: model.BlockAbort}},
		nil, nil, render.Options{HTTPListen: c.EdgeListen(), HTTPSListen: c.TLSListen()})
	if strings.Contains(string(second), `"tls"`) {
		t.Fatal("没有证书时不该渲染 apps/tls")
	}
	if _, err := cli.ApplyConfig(ctx, second); err != nil {
		t.Fatal(err)
	}

	if _, ok := c.Config()["apps"].(map[string]any)["tls"]; !ok {
		t.Fatal("没有证书的下发把节点上原有的 apps/tls 抹掉了 —— 那正是 ADR-0010 要避免的事故")
	}
}

func handshakeVia(t *testing.T, sock, sni string) tls.ConnectionState {
	t.Helper()
	raw, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		t.Fatalf("连接 TLS socket: %v", err)
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{
		ServerName: sni, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12,
	})
	defer conn.Close()
	if err := conn.Handshake(); err != nil {
		t.Fatalf("TLS 握手失败: %v", err)
	}
	return conn.ConnectionState()
}
