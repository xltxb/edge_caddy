// Package tunnel 是主控侧的 gRPC 隧道端点。
//
// Agent 主动外连（穿透 NAT），一条双向流承载全部往来。首帧必须是 Hello：
// 新节点凭一次性 Token 走服务端单向 TLS，主控在这次交换里签发隧道客户端证书；
// 此后该节点全部连接走 mTLS。见 docs/adr/0009-internal-pki-two-cas.md。
package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Heartbeat 是一次心跳上报，转给主控的其它部分。
type Heartbeat struct {
	NodeID      string
	CPU, Mem    float64
	Conns       uint32
	CfgVersion  string
	Routes      uint32
	Rules       uint32
	ReqTotal    uint64
	OriginTotal uint64
}

// ErrUnreachable 表示节点在隧道上没有回应。
var errUnreachable = errors.New("节点不可达")

// ProbeOutcome 把**隧道可达性**与**节点本机 Caddy Admin 可达性**分开报。
//
// 隧道通而 Admin 不通，说明 Caddy 挂了而 Agent 还活着——这两种故障的处置
// 完全不同（契约 §4），合成一个布尔就分不出来了。
type ProbeOutcome struct {
	RTT        time.Duration
	CaddyAdmin bool
	CfgVersion string
}

// Probe 在隧道上往返一次。
func (s *Server) Probe(ctx context.Context, nodeID string, timeout time.Duration) (ProbeOutcome, error) {
	s.mu.RLock()
	sess := s.sessions[nodeID]
	s.mu.RUnlock()
	if sess == nil {
		return ProbeOutcome{}, errUnreachable
	}
	return sess.probe(ctx, timeout)
}

// IsUnreachable 供调用方判断错误种类，不必知道内部的哨兵值。
func IsUnreachable(err error) bool { return errors.Is(err, errUnreachable) }

// ResourceCounts 随配置一起下去，Agent 记下并在心跳里报回来。
type ResourceCounts struct{ Routes, Rules uint32 }

// PushOutcome 是一次下发在单个节点上的结果。
//
// Responded 是分类重试的**唯一**依据（ADR-0005）：节点回应了但 Caddy 拒绝
// 不重试——同一份字节喂给同一个 Caddy 必然得到同样的拒绝，能修它的是人改配置，
// 不是时间。节点没回应才重试。
//
// 不去解析 Detail 的措辞来分类：那样会把重试逻辑绑死在 Caddy 的报错文案上，
// 文案一改我们就**静默**失效——不报错，只是开始做错误的重试决策。
type PushOutcome struct {
	OK        bool
	Detail    string
	Responded bool
}

type Options struct {
	Store     *store.Store
	CA        *pki.CA
	Log       *slog.Logger
	Advertise []string // 服务端证书的 SAN
	// OnHeartbeat 返回这次心跳代表的健康分档（ok / warn）。
	OnHeartbeat func(Heartbeat) string
}

type Server struct {
	edgev1.UnimplementedEdgeTunnelServer
	opt  Options
	log  *slog.Logger
	grpc *grpc.Server

	mu       sync.RWMutex
	sessions map[string]*session
}

func New(o Options) (*Server, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if len(o.Advertise) == 0 {
		o.Advertise = []string{"127.0.0.1", "localhost"}
	}

	leaf, err := o.CA.SignServer("edge-master", o.Advertise, pki.TunnelLeafTTL())
	if err != nil {
		return nil, fmt.Errorf("签发主控服务端证书: %w", err)
	}
	cert, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
	if err != nil {
		return nil, err
	}
	// 把 CA 证书也放进出示的链里。Agent 首连时手上还没有 CA，只有看得到根
	// 才能拿安装命令里的 --ca-pin 指纹去比对；不带上就只能 TOFU，
	// 而 TOFU 会让中间人在那一刻冒充主控把一次性 Token 骗走。
	if caDER, _ := pem.Decode(o.CA.CertPEM); caDER != nil {
		cert.Certificate = append(cert.Certificate, caDER.Bytes)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(o.CA.CertPEM) {
		return nil, errors.New("隧道 CA 证书无法加入信任池")
	}

	s := &Server{opt: o, log: o.Log, sessions: map[string]*session{}}
	s.grpc = grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		// VerifyClientCertIfGiven 而不是 RequireAndVerify：接入首连时节点还没有
		// 客户端证书，那一次靠一次性 Token 认证。给了证书就必须验得过。
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  roots,
		MinVersion: tls.VersionTLS12,
	})))
	edgev1.RegisterEdgeTunnelServer(s.grpc, s)
	return s, nil
}

func (s *Server) Serve(lis net.Listener) error {
	s.log.Info("gRPC 隧道监听", "addr", lis.Addr().String())
	return s.grpc.Serve(lis)
}

func (s *Server) ListenAndServe(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s: %w", addr, err)
	}
	return s.Serve(lis)
}

func (s *Server) Stop() { s.grpc.GracefulStop() }

// OnlineNodes 返回当前持有活动隧道的节点。
func (s *Server) OnlineNodes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		out = append(out, id)
	}
	return out
}

// Push 把一份配置推给一个节点并等它回报。
func (s *Server) Push(ctx context.Context, nodeID, cfgVersion string, caddyJSON, verifyRules []byte, counts ResourceCounts, deadline time.Duration) PushOutcome {
	s.mu.RLock()
	sess := s.sessions[nodeID]
	s.mu.RUnlock()
	if sess == nil {
		return PushOutcome{OK: false, Detail: "节点不在线", Responded: false}
	}
	return sess.push(ctx, cfgVersion, caddyJSON, verifyRules, counts, deadline)
}

// Channel 是隧道的全部。
func (s *Server) Channel(stream edgev1.EdgeTunnel_ChannelServer) error {
	ctx := stream.Context()

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "首帧必须是 Hello")
	}

	nodeID, enrolled, err := s.identify(ctx, hello)
	if err != nil {
		return err
	}
	if err := stream.Send(&edgev1.MasterMsg{M: &edgev1.MasterMsg_Enrolled{Enrolled: enrolled}}); err != nil {
		return err
	}

	sess := s.register(nodeID, stream)
	defer s.unregister(nodeID, sess)

	s.log.Info("节点接入", "node_id", nodeID, "agent_version", hello.GetVersion())
	if _, err := s.opt.Store.InsertEvent(ctx, nodeID, "ok", "节点已接入"); err != nil {
		s.log.Error("写接入事件失败", "err", err)
	}

	go sess.writeLoop()
	return sess.readLoop(ctx, s)
}

// identify 决定对端是谁。
//
// **证书优先于自称**：已接入的节点带着 mTLS 客户端证书，CN 就是 node_id。
// Hello 里的 node_id 只在凭 Token 首连时才被参考，而那一次 node_id 也不来自
// Agent —— 它绑定在 Token 上，签发时就定死了。
func (s *Server) identify(ctx context.Context, hello *edgev1.Hello) (string, *edgev1.Enrolled, error) {
	baseline, err := s.opt.Store.Baseline(ctx)
	if err != nil {
		return "", nil, status.Errorf(codes.Internal, "读取基线: %v", err)
	}

	if cn := clientCertCN(ctx); cn != "" {
		// 老节点：身份由证书决定，不需要 Token，也不重新签发。
		return cn, &edgev1.Enrolled{CfgVersion: baseline}, nil
	}

	if hello.GetToken() == "" {
		return "", nil, status.Error(codes.Unauthenticated,
			"没有客户端证书也没有接入 Token")
	}

	spec, err := s.opt.Store.ConsumeEnrollToken(ctx, hello.GetToken())
	switch {
	case errors.Is(err, store.ErrTokenInvalid):
		return "", nil, status.Error(codes.Unauthenticated, "接入 Token 无效")
	case errors.Is(err, store.ErrTokenExpired):
		return "", nil, status.Error(codes.Unauthenticated, "接入 Token 已过期")
	case errors.Is(err, store.ErrTokenUsed):
		return "", nil, status.Error(codes.Unauthenticated, "接入 Token 已被使用")
	case err != nil:
		return "", nil, status.Errorf(codes.Internal, "校验接入 Token: %v", err)
	}

	if err := s.opt.Store.UpsertNode(ctx, spec); err != nil {
		return "", nil, status.Errorf(codes.Internal, "写入节点: %v", err)
	}
	leaf, err := s.opt.CA.SignClient(spec.NodeID, pki.TunnelLeafTTL())
	if err != nil {
		return "", nil, status.Errorf(codes.Internal, "签发隧道证书: %v", err)
	}
	return spec.NodeID, &edgev1.Enrolled{
		TunnelCertPem: leaf.CertPEM,
		TunnelKeyPem:  leaf.KeyPEM,
		TunnelCaPem:   s.opt.CA.CertPEM,
		CfgVersion:    baseline,
	}, nil
}

func clientCertCN(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return ""
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return ""
	}
	for _, chain := range tlsInfo.State.VerifiedChains {
		if len(chain) > 0 && chain[0].Subject.CommonName != "" {
			return chain[0].Subject.CommonName
		}
	}
	return ""
}

func (s *Server) register(nodeID string, stream edgev1.EdgeTunnel_ChannelServer) *session {
	sess := newSession(nodeID, stream)

	s.mu.Lock()
	old := s.sessions[nodeID]
	s.sessions[nodeID] = sess
	s.mu.Unlock()

	if old != nil {
		// 后连接取代前连接。网络抖动后旧连接可能还没被 TCP 判死，两条流同时在，
		// 会让下发结果回报到不确定的那一条上。
		s.log.Warn("同一节点重复连接，断开旧连接", "node_id", nodeID)
		old.close()
	}
	return sess
}

func (s *Server) unregister(nodeID string, sess *session) {
	s.mu.Lock()
	if s.sessions[nodeID] == sess {
		delete(s.sessions, nodeID)
	}
	s.mu.Unlock()
	sess.close()
}
