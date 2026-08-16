package tunnel_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/enroll"
	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

// harness 起一个真实的 gRPC 服务端（真实 TLS），并提供拨号能力。
//
// 不用 mock：ADR-0009 定的接入流程里，「首连单向 TLS + Token、之后 mTLS」这一段
// 的判定点**就在 TLS 握手上**。用 mock 测等于把真正要验的那一步整个跳过。
type harness struct {
	addr     string
	srv      *tunnel.Server
	enroller *enroll.Enroller
	tunnelCA *pki.CA
	otherCA  *pki.CA // 冒充者用的、主控并不信任的 CA
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	tunnelCA, err := pki.NewCA("Edge Tunnel CA")
	if err != nil {
		t.Fatal(err)
	}
	otherCA, err := pki.NewCA("Someone Else CA")
	if err != nil {
		t.Fatal(err)
	}
	enroller := enroll.New(st)

	srv := tunnel.NewServer(tunnel.Deps{CA: tunnelCA, Enroll: enroller, Store: st})

	// 服务端证书由隧道 CA 签给主控自己
	serverCert, err := tunnelCA.IssueServer("master.local", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(serverCert.CertPEM, serverCert.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}

	// VerifyClientCertIfGiven：Enroll 允许无客户端证书（此时还没有），
	// Channel 自己要求必须有已验证的证书链。
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{pair},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    tunnelCA.Pool(),
		MinVersion:   tls.VersionTLS12,
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	edgev1.RegisterEdgeEnrollServer(g, srv)
	edgev1.RegisterEdgeTunnelServer(g, srv)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	return &harness{addr: lis.Addr().String(), srv: srv, enroller: enroller,
		tunnelCA: tunnelCA, otherCA: otherCA}
}

// dial 用给定的客户端证书拨号；certPEM 为 nil 表示不出示客户端证书。
func (h *harness) dial(t *testing.T, certPEM, keyPEM []byte) *grpc.ClientConn {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(h.tunnelCA.RootPEM())
	cfg := &tls.Config{RootCAs: roots, ServerName: "master.local", MinVersion: tls.VersionTLS12}
	if certPEM != nil {
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	cc, err := grpc.NewClient(h.addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatalf("拨号: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

// openChannel 建立 Channel 流并发一条心跳，返回服务端的反应。
func openChannel(t *testing.T, cc *grpc.ClientConn) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := edgev1.NewEdgeTunnelClient(cc).Channel(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&edgev1.AgentMsg{
		M: &edgev1.AgentMsg_Hb{Hb: &edgev1.Heartbeat{Cpu: 1, Mem: 2, CfgVersion: "cfg-aaa"}},
	}); err != nil {
		return err
	}
	// 服务端拒绝时，错误在这里浮现
	_, err = stream.Recv()
	return err
}

// 不出示客户端证书的连接不得进入隧道。
//
// 这是整个系统最重要的一条边界：能连上隧道就能收到本该发给某个节点的回源地址与
// 白名单配置。TLS 层配的是 VerifyClientCertIfGiven（Enroll 需要允许无证书接入），
// 因此「必须有证书」这件事要由 Channel 自己判定，不能指望 TLS 层。
func TestChannelRejectsConnectionWithoutClientCert(t *testing.T) {
	h := newHarness(t)
	err := openChannel(t, h.dial(t, nil, nil))
	if err == nil {
		t.Fatal("无客户端证书不应能进入隧道")
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("应返回 Unauthenticated，实际 %v（%v）", got, err)
	}
	if n := h.srv.Connected(); len(n) != 0 {
		t.Fatalf("被拒的连接不应出现在在线列表里，实际 %v", n)
	}
}

// 用主控不信任的 CA 签发的证书连不进来——这一步在 TLS 握手就该失败。
func TestChannelRejectsCertFromUntrustedCA(t *testing.T) {
	h := newHarness(t)
	forged, err := h.otherCA.IssueClient("node-hk-01", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := openChannel(t, h.dial(t, forged.CertPEM, forged.KeyPEM)); err == nil {
		t.Fatal("用未受信 CA 签的证书不应能进入隧道")
	}
	if n := h.srv.Connected(); len(n) != 0 {
		t.Fatalf("被拒的连接不应出现在在线列表里，实际 %v", n)
	}
}

// 持隧道 CA 签发的证书可以接入，且主控认到的身份来自**证书**。
func TestChannelAcceptsValidCertAndIdentityComesFromCert(t *testing.T) {
	h := newHarness(t)
	issued, err := h.tunnelCA.IssueClient("node-hk-01", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cc := h.dial(t, issued.CertPEM, issued.KeyPEM)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := edgev1.NewEdgeTunnelClient(cc).Channel(ctx)
	if err != nil {
		t.Fatalf("建立通道: %v", err)
	}
	if err := stream.Send(&edgev1.AgentMsg{
		M: &edgev1.AgentMsg_Hb{Hb: &edgev1.Heartbeat{Cpu: 15.2, Mem: 32.8, CfgVersion: "cfg-2f9a1c"}},
	}); err != nil {
		t.Fatalf("发送心跳: %v", err)
	}

	if !waitFor(3*time.Second, func() bool {
		for _, n := range h.srv.Connected() {
			if n == "node-hk-01" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("持有效证书的节点应出现在在线列表，实际 %v", h.srv.Connected())
	}
}

// 心跳消息里没有 node_id 字段，身份只可能来自证书。
//
// 这条把 proto 的设计固化成可执行的事实：留一个可自报的 node_id，等于给身份第二个
// 来源，而两个来源迟早不一致，届时「以哪个为准」就成了能被利用的问题。
// 有人日后往 Heartbeat 加回 node_id 时，这条会红。
func TestHeartbeatCarriesNoSelfReportedIdentity(t *testing.T) {
	hb := &edgev1.Heartbeat{}
	desc := hb.ProtoReflect().Descriptor()
	for i := 0; i < desc.Fields().Len(); i++ {
		if name := string(desc.Fields().Get(i).Name()); name == "node_id" {
			t.Fatal("Heartbeat 不得包含 node_id：身份只能来自客户端证书")
		}
	}
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// 完整往返，客户端是**真的 Agent**：接入换证 → 用该证书建隧道 → 心跳被主控看到。
//
// 用真 Agent 而不是手写客户端，是因为「换来的证书到底能不能用」这个风险恰恰
// 藏在客户端那侧：证书落盘的格式、TLS 配置怎么拼、连的是哪个服务。手写一份
// 只会验证「我手写的这份能用」。
func TestAgentEnrollsThenConnects(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tok, _, err := h.enroller.Issue(ctx, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	cfg := agent.Config{
		NodeID:            "node-hk-01",
		MasterAddr:        h.addr,
		ServerName:        "master.local",
		StateDir:          t.TempDir(),
		MasterCA:          h.tunnelCA.RootPEM(),
		HeartbeatInterval: 50 * time.Millisecond,
	}

	if err := agent.Enroll(ctx, cfg, tok); err != nil {
		t.Fatalf("Agent 接入应成功: %v", err)
	}
	// 私钥必须落盘为 0600：同机其他用户不该能读到它
	info, err := os.Stat(filepath.Join(cfg.StateDir, "tunnel.key"))
	if err != nil {
		t.Fatalf("隧道私钥应已落盘: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("隧道私钥权限应为 0600，实际 %o", perm)
	}

	go func() { _ = agent.Run(ctx, cfg) }()

	if !waitFor(3*time.Second, func() bool {
		for _, n := range h.srv.Connected() {
			if n == "node-hk-01" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("Agent 应出现在在线列表，实际 %v", h.srv.Connected())
	}

	// 同一个 Token 不能再换一张证书
	cfg2 := cfg
	cfg2.NodeID = "node-evil"
	cfg2.StateDir = t.TempDir()
	if err := agent.Enroll(ctx, cfg2, tok); err == nil {
		t.Fatal("已使用过的 Token 不应能再换到证书")
	}
}

// 没有隧道证书就想建连接的 Agent 必须失败，且错误要说清是没接入。
func TestAgentWithoutCertCannotConnect(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := agent.Run(ctx, agent.Config{
		NodeID: "node-hk-01", MasterAddr: h.addr, ServerName: "master.local",
		StateDir: t.TempDir(), MasterCA: h.tunnelCA.RootPEM(),
	})
	if err == nil {
		t.Fatal("没有隧道证书不应能建立连接")
	}
	if !strings.Contains(err.Error(), "接入") {
		t.Errorf("错误应提示尚未接入，实际: %v", err)
	}
	if n := h.srv.Connected(); len(n) != 0 {
		t.Fatalf("不应有节点在线，实际 %v", n)
	}
}
