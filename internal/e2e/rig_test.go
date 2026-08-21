package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/alert"
	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/caddytest"
	"github.com/xltxb/edge_caddy/internal/certs"
	"github.com/xltxb/edge_caddy/internal/deploy"
	"github.com/xltxb/edge_caddy/internal/dnsops"
	"github.com/xltxb/edge_caddy/internal/health"
	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/render"
	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
	"github.com/xltxb/edge_caddy/internal/tunnel"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// rig 是一整套跑起来的系统：主控（HTTP + gRPC 隧道）、一个真 Caddy、一个真上游。
// Agent 由测试按需启动，因为「接入」本身就是要验的东西之一。
type rig struct {
	t          *testing.T
	store      *store.Store
	http       *httptest.Server
	tunnelAddr string
	caPin      string
	caddy      *caddytest.Caddy
	upstream   string
	cookie     *http.Cookie
}

func newRig(t *testing.T) *rig {
	t.Helper()
	ctx := context.Background()

	st := testdb.New(t)
	if err := st.CreateUser(ctx, "abiu", "correct-horse"); err != nil {
		t.Fatal(err)
	}

	sealer, err := secret.New([]byte("e2e-master-key-that-is-long-enough"))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := st.EnsureCA(ctx, pki.KindTunnel, sealer)
	if err != nil {
		t.Fatal(err)
	}
	caPin, err := pki.Fingerprint(ca.CertPEM)
	if err != nil {
		t.Fatal(err)
	}

	cad := caddytest.New(t)

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "UPSTREAM OK")
	}))
	t.Cleanup(up.Close)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hub := ws.NewHub(nil)
	var monitorRef *health.Monitor
	tun, err := tunnel.New(tunnel.Options{
		Store: st, CA: ca, Advertise: []string{"127.0.0.1"},
		OnHeartbeat: func(hb tunnel.Heartbeat) string {
			if monitorRef != nil {
				return monitorRef.Observe(hb)
			}
			return "ok"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = tun.Serve(lis) }()
	t.Cleanup(tun.Stop)

	upstreamCA, err := st.EnsureCA(ctx, pki.KindUpstream, sealer)
	if err != nil {
		t.Fatal(err)
	}
	certDir := t.TempDir()

	sched := &deploy.Scheduler{
		Store: st, Pusher: tun, Hub: hub, UpstreamCA: upstreamCA,
		Render: render.Options{
			HTTPListen: cad.EdgeListen(), HTTPSListen: cad.TLSListen(),
			VerifyAddr:         cad.VerifyDial(),
			UpstreamClientCert: filepath.Join(certDir, "edge-mtls.crt"),
			UpstreamClientKey:  filepath.Join(certDir, "edge-mtls.key"),
		},
		Sealer: sealer,
	}

	monitor := health.New(health.Config{
		Store: st, Hub: hub, Interval: 200 * time.Millisecond, Threshold: 3,
	})
	monitorRef = monitor
	notifier := alert.New(st, sealer, nil)
	dnsOrch := &dnsops.Orchestrator{Store: st, Sealer: sealer}
	certMgr := certs.New(&certs.Manager{Store: st, Sealer: sealer, Hub: hub, Issuer: testIssuer{}})
	sched.EnsureCerts = certMgr.EnsureFor

	srv := httptest.NewServer(api.New(api.Options{
		Store: st, Hub: hub, Tunnel: tun, Deployer: sched,
		Health: monitor, Alerts: notifier, Sealer: sealer, DNS: dnsOrch, Certs: certMgr,
		SessionTTL: time.Hour, MasterAddr: lis.Addr().String(), CAPin: caPin,
	}))
	t.Cleanup(srv.Close)

	r := &rig{
		t: t, store: st, http: srv, tunnelAddr: lis.Addr().String(),
		caPin: caPin, caddy: cad, upstream: strings.TrimPrefix(up.URL, "http://"),
	}
	r.login()
	return r
}

type env struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
	Msg  string          `json:"msg"`
}

func (r *rig) do(method, path string, body any) (int, env) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			r.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.http.URL+"/api/v1"+path, rd)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.cookie != nil {
		req.AddCookie(r.cookie)
	}
	resp, err := r.http.Client().Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var e env
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &e); err != nil {
		r.t.Fatalf("%s %s 响应不是包裹体: %s", method, path, b)
	}
	return resp.StatusCode, e
}

// mustDo 要求业务成功；失败时把 code 与 msg 一起报出来，省得再去翻日志。
func (r *rig) mustDo(method, path string, body any) env {
	r.t.Helper()
	status, e := r.do(method, path, body)
	if status != http.StatusOK || e.Code != api.CodeOK {
		r.t.Fatalf("%s %s 失败：http=%d code=%d msg=%s", method, path, status, e.Code, e.Msg)
	}
	return e
}

func (r *rig) login() {
	r.t.Helper()
	req, _ := http.NewRequest("POST", r.http.URL+"/api/v1/auth/login",
		strings.NewReader(`{"username":"abiu","password":"correct-horse"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "ec_session" {
			r.cookie = c
			return
		}
	}
	r.t.Fatal("登录没拿到会话 Cookie")
}

// issueToken 走真实的签发端点，拿到的安装命令与运维手上的是同一条。
func (r *rig) issueToken(nodeID string) (token, caPin string) {
	r.t.Helper()
	e := r.mustDo("POST", "/nodes/token", map[string]string{
		"node_id": nodeID, "city": "香港", "vendor": "DMIT PPro",
		"line": "CN2 GIA", "public_ip": "203.0.113.7",
	})
	var d struct {
		Token      string `json:"token"`
		CAPin      string `json:"ca_pin"`
		InstallCmd string `json:"install_cmd"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		r.t.Fatal(err)
	}
	if d.Token == "" || d.CAPin == "" {
		r.t.Fatalf("签发响应缺字段: %s", e.Data)
	}
	if !strings.Contains(d.InstallCmd, d.Token) || !strings.Contains(d.InstallCmd, d.CAPin) {
		r.t.Errorf("安装命令里应当同时含 Token 与 CA 指纹: %s", d.InstallCmd)
	}
	return d.Token, d.CAPin
}

// startAgent 起一个真 Agent，跑到测试结束。stateDir 复用可模拟「重启」。
func (r *rig) startAgent(nodeID, token, stateDir string) context.CancelFunc {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a := agent.New(agent.Config{
		MasterAddr: r.tunnelAddr, NodeID: nodeID, Token: token, CAPin: r.caPin,
		StateDir: stateDir, CaddyAdmin: r.caddy.AdminURL(),
		TLSProbe:  "unix/" + r.caddy.TLSSocketPath(),
		Heartbeat: 200 * time.Millisecond,
	})
	go func() { _ = a.Run(ctx) }()
	r.t.Cleanup(cancel)
	return cancel
}

// waitOnline 等节点在 GET /nodes 里显示为在线。
func (r *rig) waitOnline(nodeID string) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, e := r.do("GET", "/nodes", nil)
		var d struct {
			Items []struct {
				ID     string `json:"id"`
				Online bool   `json:"online"`
			} `json:"items"`
		}
		_ = json.Unmarshal(e.Data, &d)
		for _, n := range d.Items {
			if n.ID == nodeID && n.Online {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.t.Fatalf("节点 %s 在 10 秒内没有上线", nodeID)
}

// waitOffline 等节点从在线变成不在线。
func (r *rig) waitOffline(nodeID string) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !r.isOnline(nodeID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.t.Fatalf("节点 %s 在 10 秒内没有离线", nodeID)
}

// stayOffline 断言节点在一段时间内**一直**没连上来。
//
// 这里必须是「持续为假」而不是「此刻为假」：Agent 本来就是断了就重连的，
// 一个瞬时的快照什么也证明不了——它可能只是拍在两次重连的空档里。
func (r *rig) stayOffline(nodeID string, d time.Duration) {
	r.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if r.isOnline(nodeID) {
			r.t.Fatalf("节点 %s 已下线，却又连回来了", nodeID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// curlVia 是最终验收：一个真请求进 Caddy，按路由回源，拿到上游的响应。
func (r *rig) curlVia(host string) (int, string) {
	r.t.Helper()
	return r.caddy.Get(host, "/", nil)
}

func (r *rig) baseline() string {
	r.t.Helper()
	e := r.mustDo("GET", "/nodes", nil)
	var d struct {
		Baseline string `json:"baseline"`
	}
	_ = json.Unmarshal(e.Data, &d)
	return d.Baseline
}

func (r *rig) isOnline(nodeID string) bool {
	r.t.Helper()
	_, e := r.do("GET", "/nodes", nil)
	var d struct {
		Items []struct {
			ID     string `json:"id"`
			Online bool   `json:"online"`
		} `json:"items"`
	}
	_ = json.Unmarshal(e.Data, &d)
	for _, n := range d.Items {
		if n.ID == nodeID && n.Online {
			return true
		}
	}
	return false
}

func (r *rig) countDeploys() int {
	r.t.Helper()
	var n int
	if err := r.store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM deploys`).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) ctx() context.Context { return context.Background() }

// deployNow 下发并返回本次的版本号。
func (r *rig) deployNow(resKeys ...string) string {
	r.t.Helper()
	e := r.mustDo("POST", "/deploys", map[string]any{"res_keys": resKeys})
	var d struct {
		CfgVersion string `json:"cfg_version"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		r.t.Fatal(err)
	}
	return d.CfgVersion
}

// issueTokenFor 给指定节点签发接入 Token。
func (r *rig) issueTokenFor(nodeID string) (token, caPin string) {
	r.t.Helper()
	e := r.mustDo("POST", "/nodes/token", map[string]string{
		"node_id": nodeID, "city": "香港", "vendor": "DMIT PPro",
		"line": "CN2 GIA", "public_ip": "203.0.113.7",
	})
	var d struct {
		Token string `json:"token"`
		CAPin string `json:"ca_pin"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		r.t.Fatal(err)
	}
	return d.Token, d.CAPin
}

// testIssuer 用内部 CA 代替真 ACME。
//
// 真 ACME 需要一个公网可解析的域名、一个真实的服务商账号，而且会在 CA 那边
// 留下真实的签发记录与速率配额消耗。要验的是**下发、加载与回执**这条链路，
// 它与证书由谁签无关——那正是把签发抽成接口的理由。
type testIssuer struct{}

func (testIssuer) Name() string { return "测试内部 CA" }

func (testIssuer) Issue(_ context.Context, domain string) ([]byte, []byte, time.Time, error) {
	ca, err := pki.GenerateCA(pki.KindTunnel)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	leaf, err := ca.SignServer(domain, []string{domain}, 90*24*time.Hour)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return leaf.CertPEM, leaf.KeyPEM, time.Now().Add(90 * 24 * time.Hour), nil
}
