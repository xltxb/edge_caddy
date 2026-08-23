package e2e_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
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

	// dnsCalls 数假服务商收到了几个请求。**「推没推」只有在这一侧才看得出来**：
	// 接口回 200 说明它没报错，不说明它真去推了。
	dnsCalls *int32
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
	// 假 DNSPod。没有它，下线的「排空连接」那一步在 e2e 里一次也走不到——
	// 排空只在上一步真的摘掉了解析时才执行，而真服务商这里配不了。
	//
	// 光有这个 server 还不够，得有测试**真的去配**它（configureDNSProvider）；
	// 不配的话 Sync 仍然返回 ErrNoProvider，跟从前一样。
	var dnsCalls int32
	dnsAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&dnsCalls, 1)
		body := `{"status":{"code":"10","message":"No records"}}`
		if r.URL.Path != "/Record.List" {
			body = `{"status":{"code":"1","message":"Action completed successful"}}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(dnsAPI.Close)

	dnsOrch := &dnsops.Orchestrator{Store: st, Sealer: sealer, BaseOverride: dnsAPI.URL}
	certMgr := certs.New(&certs.Manager{Store: st, Sealer: sealer, Hub: hub})

	srv := httptest.NewServer(api.New(api.Options{
		Store: st, Hub: hub, Tunnel: tun, Deployer: sched,
		Health: monitor, Alerts: notifier, Sealer: sealer, DNS: dnsOrch, Certs: certMgr,
		SessionTTL: time.Hour, MasterAddr: lis.Addr().String(), CAPin: caPin,
	}))
	t.Cleanup(srv.Close)

	r := &rig{
		t: t, store: st, http: srv, tunnelAddr: lis.Addr().String(),
		caPin: caPin, caddy: cad, upstream: strings.TrimPrefix(up.URL, "http://"),
		dnsCalls: &dnsCalls,
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
	return r.startAgentAt(r.tunnelAddr, nodeID, token, stateDir)
}

// startAgentOverWS 让节点走 **HTTP 面上那条隧道**（`ws://…/api/v1/tunnel`），
// 而不是直连 gRPC 端口。
//
// 里层完全一样：同一套 mTLS、同一个内部 CA、同一个 CA pin、同一份 gRPC。
// 所以这两条路径应当在**每一件事**上表现一致，而不只是「能连上」。
func (r *rig) startAgentOverWS(nodeID, token, stateDir string) context.CancelFunc {
	return r.startAgentAt("ws://"+strings.TrimPrefix(r.http.URL, "http://"),
		nodeID, token, stateDir)
}

func (r *rig) startAgentAt(master, nodeID, token, stateDir string) context.CancelFunc {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	// 日志缓冲接上：不接的话 #26 那条链路在 e2e 里一次也走不到，
	// 而它只在「Logs 非 nil」时才启动——跟 DNS 那次是同一个形状。
	logs := &agent.LogBuffer{}
	a := agent.New(agent.Config{
		MasterAddr: master, NodeID: nodeID, Token: token, CAPin: r.caPin,
		StateDir: stateDir, CaddyAdmin: r.caddy.AdminURL(),
		TLSProbe:  "unix/" + r.caddy.TLSSocketPath(),
		Heartbeat: 200 * time.Millisecond,
		Log:       slog.New(logs.Handler(slog.NewTextHandler(io.Discard, nil))),
		Logs:      logs,
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

// configureDNSProvider 配上假服务商，让「解析真的变了」这条分支能被走到。
func (r *rig) configureDNSProvider() {
	r.t.Helper()
	r.mustDo("PUT", "/settings", map[string]any{
		"dns_provider": map[string]any{
			"kind": "dnspod", "domain": "example.com", "sub": "cdn",
			"credential": "fake-token",
		},
	})
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

// isOnline 回答一个节点此刻在不在线。
//
// **它不吞错误，这是刻意的。** 原先这里写着 `_ = json.Unmarshal(...)`，
// 于是 /nodes 请求失败、响应结构变了、Data 是 null——任何一种情况下 Items 都是空，
// 函数返回 false，而 stayOffline 会一路绿地说「很好，节点确实没连回来」。
//
// 这是否定断言的第三种坏法（前端 agent 命名的）：不是装置没打开，
// 也不是断言比意图宽，而是**断言被「对象消失」满足，不是被「对象正确」满足**。
// 三种的共同点是「绿」这个信号被别的东西冒领了。
//
// waitOnline 不吃这一套——它要求 Online 为真，装置坏了它会超时报错。
// 又一次印证：肯定断言天然带自检。
func (r *rig) isOnline(nodeID string) bool {
	r.t.Helper()
	status, e := r.do("GET", "/nodes", nil)
	if status != 200 || e.Code != 0 {
		r.t.Fatalf("GET /nodes 失败（http=%d code=%d msg=%q）—— "+
			"在线判定拿不到数据时必须炸，不能静静地回 false", status, e.Code, e.Msg)
	}
	var d struct {
		Items []struct {
			ID     string `json:"id"`
			Online bool   `json:"online"`
		} `json:"items"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		r.t.Fatalf("解析 /nodes 失败：%v\n%s", err, e.Data)
	}
	if len(d.Items) == 0 {
		// 调用方都是在节点接入过之后才问的。一个空列表意味着这份响应
		// 不是我们以为的东西，而不是「节点不在线」。
		r.t.Fatalf("/nodes 一个节点都没有 —— 这份响应不是我们以为的东西：%s", e.Data)
	}
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

// importableCert 造一张由「外部平台 CA」签出来的叶子证书，
// 用来测导入接口。**不用自签**：自签证书的签发者是它自己，
// 而导入接口回报的 issuer 正是要让人认出「这张是谁签的」。
func importableCert(t *testing.T, domain string) (certPEM, keyPEM []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "外部证书平台 CA"},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(crand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(crand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	inter := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	return append(leaf, inter...),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

// dnsHits 是假服务商到目前为止收到的请求数。
func (r *rig) dnsHits() int { return int(atomic.LoadInt32(r.dnsCalls)) }
