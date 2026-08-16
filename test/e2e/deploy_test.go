// Package e2e 把主控、Agent 与真实 Caddy 串起来跑完整链路。
package e2e

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/deploy"
	"github.com/xltxb/edge_caddy/internal/enroll"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/render"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/tunnel"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// 建一条路由 → 下发 → 节点上的真实 Caddy 生效 → curl 命中上游。
//
// 这条串的是三个真进程内的真组件：主控（gRPC + 编排）、Agent（隧道 + Caddy 客户端）、
// 真实 Caddy。任何一环用假的都会漏掉这条测试真正要证明的事——渲染产物能让流量
// 真正跑通。它同时守住「下发不得抹掉节点上其他 app」：Caddy 里预先放了一个 tls app。
func TestDeployMakesTrafficFlow(t *testing.T) {
	caddyBin := findCaddy(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "UPSTREAM host=%s", r.Host)
	}))
	defer upstream.Close()

	edgePort, adminPort := freePort(t), freePort(t)
	caddyDir := t.TempDir()
	startCaddy(t, caddyBin, caddyDir, adminPort)

	// 预先往 Caddy 里放一个 tls app，代表外部证书平台写入的内容。
	// 下发之后它必须还在——上一版就是在这里把节点上所有证书抹掉的。
	seedTLSApp(t, adminPort, caddyDir)

	m := startMaster(t, render.Options{Listen: []string{fmt.Sprintf("127.0.0.1:%d", edgePort)}})
	tok, _, err := m.enroller.Issue(context.Background(), 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	agentDir := t.TempDir()
	cfg := agent.Config{
		NodeID: "node-e2e-01", MasterAddr: m.addr, ServerName: "master.local",
		StateDir: agentDir, MasterCA: m.ca.RootPEM(),
		CaddyAdmin:        fmt.Sprintf("http://127.0.0.1:%d", adminPort),
		HeartbeatInterval: 100 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := agent.Enroll(ctx, cfg, tok); err != nil {
		t.Fatalf("接入失败: %v", err)
	}
	go func() { _ = agent.Run(ctx, cfg) }()

	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 1 }) {
		t.Fatal("Agent 未能接入")
	}

	// 建一条指向上游的路由。监听端口用非特权端口，其余走真实渲染。
	if err := m.st.PutRoute(ctx, model.Route{
		Domain: "e2e.example.com", Upstream: hostPort(upstream.URL),
		Block: model.BlockAbort, BodyMax: "5MB", Compress: true,
		Whitelist: []string{model.AllowAllCIDR},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := m.orch.Deploy(ctx, "abiu", nil)
	if err != nil {
		t.Fatalf("下发失败: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0].State != "ok" {
		t.Fatalf("下发应在该节点上成功，实际 %+v", res.Rows)
	}
	t.Logf("下发耗时: %s", res.Rows[0].Detail)

	// 真实流量：curl 打边缘端口，必须命中上游
	if !waitFor(5*time.Second, func() bool {
		code, body := getVia(edgePort, "e2e.example.com")
		return code == http.StatusOK && len(body) > 8 && body[:8] == "UPSTREAM"
	}) {
		code, body := getVia(edgePort, "e2e.example.com")
		t.Fatalf("流量未能通过边缘到达上游：HTTP %d，响应体 %q", code, body)
	}

	// 节点上其他 app 必须存活
	names := appNames(t, adminPort)
	if !hasName(names, "tls") {
		t.Fatalf("下发抹掉了节点上的 tls app——所有证书失效；现存 %v", names)
	}
	if !hasName(names, "http") {
		t.Fatalf("下发后没有 http app；现存 %v", names)
	}

	// 心跳应带上新的配置版本，主控据此判断漂移
	if !waitFor(3*time.Second, func() bool {
		ns, err := m.st.ListNodes(ctx)
		return err == nil && len(ns) == 1 && ns[0].CfgVersion == res.CfgVersion
	}) {
		ns, _ := m.st.ListNodes(ctx)
		t.Fatalf("节点应上报新的配置版本 %s，实际 %+v", res.CfgVersion, ns)
	}
}

// ── 测试装置 ──

type master struct {
	addr     string
	st       *store.Store
	tun      *tunnel.Server
	orch     *deploy.Orchestrator
	enroller *enroll.Enroller
	ca       *pki.CA
	hub      *ws.Hub
}

func startMaster(t *testing.T, opts render.Options) *master {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "e2e.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ca, err := pki.NewCA("Edge Tunnel CA")
	if err != nil {
		t.Fatal(err)
	}
	enroller := enroll.New(st)
	hub := ws.NewHub()
	tun := tunnel.NewServer(tunnel.Deps{CA: ca, Enroll: enroller, Store: st, Hub: hub})
	orch := deploy.NewWith(st, tun, render.Options{Listen: opts.Listen}, nil)
	tun.SetResults(orch)
	orch.SetBroadcaster(hub)

	cert, err := ca.IssueServer("master.local", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(cert.CertPEM, cert.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{pair},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    ca.Pool(),
		MinVersion:   tls.VersionTLS12,
	})))
	edgev1.RegisterEdgeEnrollServer(g, tun)
	edgev1.RegisterEdgeTunnelServer(g, tun)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	return &master{addr: lis.Addr().String(), st: st, tun: tun, orch: orch, enroller: enroller, ca: ca, hub: hub}
}

func startCaddy(t *testing.T, bin, dir string, adminPort int) {
	t.Helper()
	cfgPath := filepath.Join(dir, "init.json")
	init := fmt.Sprintf(`{"admin":{"listen":"127.0.0.1:%d"},"apps":{}}`, adminPort)
	if err := os.WriteFile(cfgPath, []byte(init), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "run", "--config", cfgPath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 caddy: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if !waitFor(5*time.Second, func() bool {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/config/", adminPort))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}) {
		t.Fatal("caddy 未能就绪")
	}
}

func seedTLSApp(t *testing.T, adminPort int, dir string) {
	t.Helper()
	// 只要求 Caddy 接受这段配置，不需要真证书文件——load_files 在这里
	// 只是一个「外部平台写入过东西」的标记。
	body := fmt.Sprintf(`{"certificates":{"load_files":[{"certificate":%q,"key":%q}]}}`,
		filepath.Join(dir, "x.crt"), filepath.Join(dir, "x.key"))
	writePEMPair(t, dir)
	post(t, fmt.Sprintf("http://127.0.0.1:%d/config/apps/tls", adminPort), body)
}

func writePEMPair(t *testing.T, dir string) {
	t.Helper()
	out, err := exec.Command("openssl", "req", "-x509", "-newkey", "rsa:2048",
		"-keyout", filepath.Join(dir, "x.key"), "-out", filepath.Join(dir, "x.crt"),
		"-days", "1", "-nodes", "-subj", "/CN=seed.example.com").CombinedOutput()
	if err != nil {
		t.Fatalf("生成占位证书: %v\n%s", err, out)
	}
}

func post(t *testing.T, url, body string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", stringsReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s 失败 HTTP %d: %s", url, resp.StatusCode, blob)
	}
}

func appNames(t *testing.T, adminPort int) []string {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/config/apps", adminPort))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var apps map[string]json.RawMessage
	blob, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(blob, &apps)
	out := make([]string, 0, len(apps))
	for k := range apps {
		out = append(out, k)
	}
	return out
}

func getVia(port int, host string) (int, string) {
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	req.Host = host
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(blob)
}

func findCaddy(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("EDGE_TEST_CADDY"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("caddy"); err == nil {
		return p
	}
	t.Skip("未找到 caddy 二进制，「配置下发后流量能否真正跑通」这条路径未被覆盖；" +
		"设 EDGE_TEST_CADDY 指向 caddy 后重跑")
	return ""
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func hostPort(u string) string { return u[len("http://"):] }

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

type sr struct {
	s string
	i int
}

func stringsReader(s string) io.Reader { return &sr{s: s} }

func (r *sr) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

// ── 小工具 ──

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }

// pemEncodeCert 把 DER 证书转成 PEM 主体（不含头尾行），
// 供 Caddy 的 inline ca_pool 使用——它要的是 base64 主体。
func pemEncodeCert(der []byte) string {
	return base64.StdEncoding.EncodeToString(der)
}
