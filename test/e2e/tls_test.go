package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/certs"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
)

// 证书经隧道下发到节点，:443 真的提供 TLS，客户端能校验证书链。
//
// 这条串的是真主控 + 真 Agent + 真 Caddy + 真 TLS 握手。证书用测试里现场生成的
// 自签 CA——ACME 那一步需要真实域名与 DNS 凭据，本机跑不了，但**从证书进库到
// 浏览器能验证它**这一整段是可以真跑的，而那正是最容易出错的部分。
func TestCertReachesNodeAndTLSWorks(t *testing.T) {
	caddyBin := findCaddy(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "UPSTREAM-OVER-TLS")
	}))
	defer upstream.Close()

	edgePort, adminPort := freePort(t), freePort(t)
	startCaddy(t, caddyBin, t.TempDir(), adminPort)

	m := startMaster(t, render.Options{Listen: []string{fmt.Sprintf("127.0.0.1:%d", edgePort)}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	joinAgent(t, ctx, m, "node-tls-01", adminPort)
	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 1 }) {
		t.Fatal("Agent 未能接入")
	}

	const domain = "tls.example.com"
	ca, leaf := issueLeaf(t, domain)
	if err := certs.Save(ctx, m.st, m.secret, leaf); err != nil {
		t.Fatal(err)
	}
	if err := m.st.PutRoute(ctx, model.Route{
		Domain: domain, Upstream: hostPort(upstream.URL),
		Block: model.BlockAbort, BodyMax: "1MB", Whitelist: []string{model.AllowAllCIDR},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := m.orch.Deploy(ctx, "abiu", nil)
	if err != nil {
		t.Fatalf("下发失败: %v", err)
	}
	if res.Rows[0].State != "ok" {
		t.Fatalf("下发应成功，实际 %+v", res.Rows[0])
	}

	// 真 TLS 握手 + 真证书链校验。跳过校验的话，这条测试就退化成
	// 「端口上有东西在应答」，而证书装没装对完全看不出来。
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: domain, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", edgePort))
		},
	}}
	defer client.CloseIdleConnections()

	var body string
	okTLS := waitFor(8*time.Second, func() bool {
		resp, err := client.Get("https://" + domain + "/")
		if err != nil {
			body = err.Error()
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body = string(b)
		return resp.StatusCode == http.StatusOK && body == "UPSTREAM-OVER-TLS"
	})
	if !okTLS {
		t.Fatalf("HTTPS 未能跑通：%s", body)
	}
}

// 没有证书时**不渲染 tls app**，节点上原有的 tls 配置原样保留。
//
// 我们只在自己持有证书时才接管 apps/tls。反过来的话，一个还没签发证书的
// 系统会把节点上外部证书平台写入的内容抹掉——那是上一版真出过的事故。
func TestNoCertsLeavesNodeTLSAppAlone(t *testing.T) {
	caddyBin := findCaddy(t)
	edgePort, adminPort := freePort(t), freePort(t)
	caddyDir := t.TempDir()
	startCaddy(t, caddyBin, caddyDir, adminPort)
	seedTLSApp(t, adminPort, caddyDir)

	m := startMaster(t, render.Options{Listen: []string{fmt.Sprintf("127.0.0.1:%d", edgePort)}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	joinAgent(t, ctx, m, "node-notls-01", adminPort)
	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 1 }) {
		t.Fatal("Agent 未能接入")
	}

	if err := m.st.PutRoute(ctx, model.Route{
		Domain: "plain.example.com", Upstream: "127.0.0.1:1",
		Block: model.BlockAbort, BodyMax: "1MB", Whitelist: []string{model.AllowAllCIDR},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.orch.Deploy(ctx, "abiu", nil); err != nil {
		t.Fatal(err)
	}

	if names := appNames(t, adminPort); !hasName(names, "tls") {
		t.Fatalf("没有证书时不该动节点上的 tls app，现存 %v", names)
	}
}

// 新节点接入后能拿到已有域名的证书，不需要人工干预。
//
// 证书随每次下发一起带上，而不是「签发那一刻推一次」——后者会让接入时间晚于
// 签发的节点永远拿不到证书，而现象是「那台机器上的 HTTPS 不通」。
func TestNewNodeGetsExistingCerts(t *testing.T) {
	caddyBin := findCaddy(t)
	edgePort := freePort(t)
	adminA, adminB := freePort(t), freePort(t)
	startCaddy(t, caddyBin, t.TempDir(), adminA)

	m := startMaster(t, render.Options{Listen: []string{fmt.Sprintf("127.0.0.1:%d", edgePort)}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const domain = "late.example.com"
	_, leaf := issueLeaf(t, domain)
	if err := certs.Save(ctx, m.st, m.secret, leaf); err != nil {
		t.Fatal(err)
	}
	if err := m.st.PutRoute(ctx, model.Route{
		Domain: domain, Upstream: "127.0.0.1:1",
		Block: model.BlockAbort, BodyMax: "1MB", Whitelist: []string{model.AllowAllCIDR},
	}); err != nil {
		t.Fatal(err)
	}

	joinAgent(t, ctx, m, "node-early-01", adminA)
	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 1 }) {
		t.Fatal("首个节点未接入")
	}
	if _, err := m.orch.Deploy(ctx, "abiu", nil); err != nil {
		t.Fatal(err)
	}

	// 证书签发之后才接入的节点
	startCaddy(t, caddyBin, t.TempDir(), adminB)
	joinAgent(t, ctx, m, "node-late-01", adminB)
	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 2 }) {
		t.Fatal("第二个节点未接入")
	}
	res, err := m.orch.Deploy(ctx, "abiu", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Rows {
		if r.State != "ok" {
			t.Fatalf("%s 下发失败：%s", r.NodeID, r.Detail)
		}
	}

	// 后接入的节点上必须有 tls app，且里面有那张证书
	if names := appNames(t, adminB); !hasName(names, "tls") {
		t.Fatalf("后接入的节点应拿到证书，现存 app %v", names)
	}
	blob := appConfig(t, adminB, "tls")
	if !strings.Contains(blob, "CERTIFICATE") {
		t.Errorf("后接入节点的 tls app 里没有证书内容：%s", blob)
	}
}

// ── 装置 ──

// issueLeaf 现场造一个 CA 和一张叶子证书，返回 CA 证书与可入库的 Cert。
func issueLeaf(t *testing.T, domain string) (*x509.Certificate, certs.Cert) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "E2E Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	// 带上中间/根：少了链，客户端会报「证书链不完整」
	chain := append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...,
	)
	c, err := certs.Parse(domain, chain,
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	return caCert, c
}

// appConfig 读回节点上某个 app 的配置原文。
func appConfig(t *testing.T, adminPort int, name string) string {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/config/apps/%s", adminPort, name))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(blob)
}
