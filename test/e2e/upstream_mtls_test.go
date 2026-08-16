package e2e

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/render"
)

// 源站**要求客户端证书**，边缘持主控签发的叶子过关；不带证书则被源站拒。
//
// 这是 #6 的全部意义：单测能证明证书签得对，但证明不了「这张证书真能过源站
// 那一关」——TLS 握手、证书链、扩展用途，任何一处不对都在这里才会暴露。
func TestUpstreamMTLSAgainstRealOrigin(t *testing.T) {
	caddyBin := findCaddy(t)

	// 回源 CA 与它签的叶子
	ca, err := pki.NewCA("Edge Upstream CA")
	if err != nil {
		t.Fatal(err)
	}
	iss := pki.NewUpstreamIssuer(ca, nil)
	leaf, err := iss.EnsureFor(t.Context(), "node-mtls-01")
	if err != nil {
		t.Fatal(err)
	}

	// 一个真的要求客户端证书的源站
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.RootPEM()) {
		t.Fatal("源站信任库加载失败")
	}
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cn := "none"
		if len(r.TLS.PeerCertificates) > 0 {
			cn = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		fmt.Fprintf(w, "ORIGIN client=%s", cn)
	}))
	origin.TLS = &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  pool,
		MinVersion: tls.VersionTLS12,
	}
	origin.StartTLS()
	defer origin.Close()

	// 证书落盘到 Agent 会写的位置（测试里改指到临时目录）
	dir := t.TempDir()
	oldCert, oldKey := render.UpstreamCertPath, render.UpstreamKeyPath
	render.UpstreamCertPath = filepath.Join(dir, "upstream.crt")
	render.UpstreamKeyPath = filepath.Join(dir, "upstream.key")
	t.Cleanup(func() { render.UpstreamCertPath, render.UpstreamKeyPath = oldCert, oldKey })
	if err := os.WriteFile(render.UpstreamCertPath, leaf.CertPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(render.UpstreamKeyPath, leaf.KeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	// 真 Caddy，配置由真渲染器产出
	edgePort, adminPort := freePort(t), freePort(t)
	startCaddy(t, caddyBin, t.TempDir(), adminPort)

	originHost := origin.URL[len("https://"):]
	routes := []model.Route{
		{Domain: "mtls.example.com", Upstream: originHost, MTLS: true,
			Block: model.BlockAbort, BodyMax: "1MB", Whitelist: []string{model.AllowAllCIDR}},
		{Domain: "plain.example.com", Upstream: originHost, MTLS: false,
			Block: model.BlockAbort, BodyMax: "1MB", Whitelist: []string{model.AllowAllCIDR}},
	}
	blob, err := render.CaddyWith(routes, render.Options{
		Listen: []string{fmt.Sprintf("127.0.0.1:%d", edgePort)},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 源站用的是自签证书，让 Caddy 信任它（生产里源站是可信证书）
	blob = injectUpstreamTrust(t, blob, origin.Certificate().Raw)

	if _, err := agent.NewCaddyClient(fmt.Sprintf("http://127.0.0.1:%d", adminPort)).
		Apply(t.Context(), blob); err != nil {
		t.Fatalf("下发失败: %v", err)
	}

	get := func(host string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", edgePort), nil)
		req.Host = host
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		buf := make([]byte, 256)
		n, _ := resp.Body.Read(buf)
		return resp.StatusCode, string(buf[:n])
	}

	// 开了 mTLS 的路由：源站认出了这台节点
	code, body := get("mtls.example.com")
	if code != http.StatusOK {
		t.Fatalf("开启回源 mTLS 后应能通过源站的客户端证书校验，实际 %d（%s）", code, body)
	}
	if body != "ORIGIN client=node-mtls-01" {
		t.Fatalf("源站应认出该节点的身份，实际响应体 %q", body)
	}

	// 没开 mTLS 的路由：源站要求证书，因此必然失败——
	// 这条同时证明上面那条不是「源站根本没在校验」。
	if code, _ := get("plain.example.com"); code == http.StatusOK {
		t.Error("未开启 mTLS 的路由不应能通过要求客户端证书的源站——" +
			"若它也通过了，说明源站压根没在校验，上面那条断言就没有意义")
	}
}

// injectUpstreamTrust 让 Caddy 信任测试源站的自签证书。
func injectUpstreamTrust(t *testing.T, blob []byte, originDER []byte) []byte {
	t.Helper()
	pemBlob := pemEncodeCert(originDER)
	var doc map[string]any
	if err := jsonUnmarshal(blob, &doc); err != nil {
		t.Fatal(err)
	}
	srv := doc["http"].(map[string]any)["servers"].(map[string]any)["edge"].(map[string]any)
	for _, r := range srv["routes"].([]any) {
		sub := r.(map[string]any)["handle"].([]any)[0].(map[string]any)
		for _, br := range sub["routes"].([]any) {
			hs, ok := br.(map[string]any)["handle"].([]any)
			if !ok {
				continue
			}
			for _, h := range hs {
				hm := h.(map[string]any)
				if hm["handler"] != "reverse_proxy" {
					continue
				}
				tr := hm["transport"].(map[string]any)
				tlsCfg, _ := tr["tls"].(map[string]any)
				if tlsCfg == nil {
					tlsCfg = map[string]any{}
					tr["tls"] = tlsCfg
				}
				tlsCfg["ca"] = map[string]any{
					"provider": "inline", "trusted_ca_certs": []any{pemBlob},
				}
			}
		}
	}
	out, err := jsonMarshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
