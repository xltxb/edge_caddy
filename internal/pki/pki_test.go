package pki_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/pki"
)

func mustCA(t *testing.T, kind pki.Kind) *pki.CA {
	t.Helper()
	ca, err := pki.GenerateCA(kind)
	if err != nil {
		t.Fatalf("生成 %s CA: %v", kind, err)
	}
	return ca
}

func pool(t *testing.T, ca *pki.CA) *x509.CertPool {
	t.Helper()
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(ca.CertPEM) {
		t.Fatal("CA 证书无法加入信任池")
	}
	return p
}

func keypair(t *testing.T, leaf *pki.Leaf) tls.Certificate {
	t.Helper()
	c, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
	if err != nil {
		t.Fatalf("装配 keypair: %v", err)
	}
	return c
}

// handshake 起一个真 TLS 服务端并用给定的客户端配置连它。
//
// 两侧的错误都要看：TLS 1.3 下客户端证书是在客户端认为握手已完成**之后**才发出的，
// 服务端的拒绝以 alert 到达，只在客户端侧判错会漏掉它——那会让一个「谁都能连上」
// 的严重缺陷在测试里显示为通过。
func handshake(t *testing.T, srv *tls.Config, cli *tls.Config) error {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srv)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	srvErr := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			srvErr <- err
			return
		}
		defer c.Close()
		tc := c.(*tls.Conn)
		if err := tc.HandshakeContext(context.Background()); err != nil {
			srvErr <- err
			return
		}
		_, err = io.WriteString(tc, "ok")
		srvErr <- err
	}()

	d := &net.Dialer{Timeout: 3 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", ln.Addr().String(), cli)
	if err != nil {
		<-srvErr
		return err
	}
	defer conn.Close()

	// 必须真的读一次，才会收到服务端因客户端证书不被信任而发的 alert。
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, readErr := io.ReadAll(io.LimitReader(conn, 2))

	if e := <-srvErr; e != nil {
		return fmt.Errorf("服务端: %w", e)
	}
	if readErr != nil {
		return fmt.Errorf("客户端读: %w", readErr)
	}
	return nil
}

// 隧道 mTLS 真的能握手：主控出示 CA 签的服务端证书，Agent 出示 CA 签的客户端证书。
func TestTunnelMTLSHandshakeSucceeds(t *testing.T) {
	ca := mustCA(t, pki.KindTunnel)

	srvLeaf, err := ca.SignServer("edge-master", []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cliLeaf, err := ca.SignClient("node-hk-01", pki.TunnelLeafTTL())
	if err != nil {
		t.Fatal(err)
	}

	err = handshake(t,
		&tls.Config{
			Certificates: []tls.Certificate{keypair(t, srvLeaf)},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    pool(t, ca),
			MinVersion:   tls.VersionTLS12,
		},
		&tls.Config{
			Certificates: []tls.Certificate{keypair(t, cliLeaf)},
			RootCAs:      pool(t, ca),
			ServerName:   "127.0.0.1",
			MinVersion:   tls.VersionTLS12,
		})
	if err != nil {
		t.Fatalf("隧道 mTLS 握手应当成功: %v", err)
	}
}

// 两套 CA 相互独立（ADR-0009）：回源 CA 签的证书连不上隧道。
//
// 这条锁住的不是一个字段，而是那条 ADR 的核心——共用一个根意味着源站侧的一次
// 换根会同时把所有节点踢下控制面，而那正是最需要控制面的时刻。
func TestUpstreamCACannotAuthenticateToTunnel(t *testing.T) {
	tunnelCA := mustCA(t, pki.KindTunnel)
	upstreamCA := mustCA(t, pki.KindUpstream)

	srvLeaf, _ := tunnelCA.SignServer("edge-master", []string{"127.0.0.1"}, time.Hour)
	// 用**回源** CA 给节点签一张客户端证书，拿它去连隧道。
	cliLeaf, _ := upstreamCA.SignClient("node-hk-01", time.Hour)

	err := handshake(t,
		&tls.Config{
			Certificates: []tls.Certificate{keypair(t, srvLeaf)},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    pool(t, tunnelCA),
			MinVersion:   tls.VersionTLS12,
		},
		&tls.Config{
			Certificates: []tls.Certificate{keypair(t, cliLeaf)},
			RootCAs:      pool(t, tunnelCA),
			ServerName:   "127.0.0.1",
			MinVersion:   tls.VersionTLS12,
		})
	if err == nil {
		t.Fatal("回源 CA 签的证书不该能通过隧道的客户端校验——两套 CA 就白分了")
	}
	// **err != nil 还不够。** 端口分配失败、握手超时、keypair 出错，
	// 全都会让 err 非 nil，而这条测试想证的是「证书被拒了」，不是「没连上」。
	//
	// 这是一条否定断言，它天然会因为装置失效而变绿——旁边的
	// TestTunnelMTLSHandshakeSucceeds 用同一套装置做正面对照，装置坏了那条会红。
	// 但那是偶然的兜底：这条自己读起来完全独立。所以把「因为什么失败」也钉住。
	if !strings.Contains(err.Error(), "certificate") &&
		!strings.Contains(err.Error(), "bad certificate") &&
		!strings.Contains(err.Error(), "tls:") {
		t.Fatalf("失败原因得是证书被拒，而不是别的什么故障：%v", err)
	}
}

// 客户端证书的 CN 是 node_id：隧道上的身份来自它出示的证书，
// 而不是 Agent 在 Hello 里的自称。
func TestClientCertCarriesNodeIDAsCommonName(t *testing.T) {
	ca := mustCA(t, pki.KindTunnel)
	leaf, err := ca.SignClient("node-tw-01", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c := keypair(t, leaf)
	parsed, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Subject.CommonName != "node-tw-01" {
		t.Fatalf("CN = %q，想要 node-tw-01", parsed.Subject.CommonName)
	}
}

// 隧道叶子取长期。短了会死锁：证书过期 → 连不上主控 → 拿不到新证书 → 永远连不上。
func TestTunnelLeafIsLongLived(t *testing.T) {
	if got := pki.TunnelLeafTTL(); got < 365*24*time.Hour {
		t.Fatalf("隧道叶子 TTL = %v，短于一年会有死锁风险（ADR-0009）", got)
	}
}

// 指纹用于安装命令里的 --ca-pin：Agent 首连时手上还没有 CA，
// 纯 TOFU 会让中间人在那一刻冒充主控把一次性 Token 骗走。
func TestFingerprintIsStableAndDistinct(t *testing.T) {
	a := mustCA(t, pki.KindTunnel)
	b := mustCA(t, pki.KindTunnel)

	fa1, err := pki.Fingerprint(a.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	fa2, _ := pki.Fingerprint(a.CertPEM)
	fb, _ := pki.Fingerprint(b.CertPEM)

	if fa1 != fa2 {
		t.Fatal("同一张证书两次取指纹不一致")
	}
	if fa1 == fb {
		t.Fatal("两套不同的 CA 指纹相同")
	}
	if len(fa1) != 64 {
		t.Fatalf("指纹长度 = %d，想要 64（SHA-256 十六进制）", len(fa1))
	}
}
