// Package tunnel 是主控侧的节点长连接。
//
// 两个服务跑在同一个监听器上，凭据要求不同（docs/adr/0009）：
//
//	EdgeEnroll —— 接入用。节点此时还没有证书，因此允许无客户端证书，用一次性 Token 换证书。
//	EdgeTunnel —— 长连接。**必须**出示由隧道 CA 签发的客户端证书。
//
// TLS 层配的是 VerifyClientCertIfGiven（Enroll 需要放行无证书的连接），所以
// 「Channel 必须有证书」这件事由 Channel 自己判定，不能指望 TLS 层替它把关。
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/enroll"
	"github.com/xltxb/edge_caddy/internal/pki"
)

// ErrNodeNotConnected 表示目标节点当前没有活跃连接。
var ErrNodeNotConnected = errors.New("节点未连接")

// TunnelCertTTL 是隧道客户端证书的有效期。
//
// 取长期（而非回源证书那样的 24 小时）是因为会死锁：隧道证书过期 → 连不上主控 →
// 拿不到新证书 → 永远连不上。续期在远早于到期时通过隧道完成；真过期了的恢复
// 路径是重新走接入流程（docs/adr/0009）。
const TunnelCertTTL = 365 * 24 * time.Hour

// Store 是 tunnel 需要的存储能力。
type Store interface {
	UpsertNodeSeen(ctx context.Context, id, cfgVersion string, now time.Time) error
	MarkNodeDown(ctx context.Context, id string) error
}

type Deps struct {
	CA     *pki.CA
	Enroll *enroll.Enroller
	Store  Store
	Logger *slog.Logger
}

type session struct {
	send chan *edgev1.MasterMsg
}

type Server struct {
	edgev1.UnimplementedEdgeEnrollServer
	edgev1.UnimplementedEdgeTunnelServer

	deps Deps
	log  *slog.Logger

	mu       sync.RWMutex
	sessions map[string]*session
}

func NewServer(d Deps) *Server {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{deps: d, log: log, sessions: map[string]*session{}}
}

// Enroll 用一次性 Token 换取隧道客户端证书。
//
// 这个 RPC 允许在没有客户端证书的连接上调用——节点此刻正是为了拿证书而来。
// 它的门是 Token，且 Token 的作废与校验是同一个原子操作。
func (s *Server) Enroll(ctx context.Context, req *edgev1.EnrollRequest) (*edgev1.EnrollResponse, error) {
	nodeID := req.GetNodeId()
	if err := s.deps.Enroll.Consume(ctx, req.GetToken(), nodeID); err != nil {
		// 对外只说「接入凭据无效」，不区分未知/已用/过期——区分等于告诉试探者
		// 他手上的串是不是曾经有效过。详细原因留给日志。
		s.log.Warn("节点接入被拒绝", "node_id", nodeID, "peer", peerAddr(ctx), "err", err)
		return nil, status.Error(codes.PermissionDenied, "接入凭据无效")
	}

	issued, err := s.deps.CA.IssueClient(nodeID, TunnelCertTTL)
	if err != nil {
		s.log.Error("签发隧道证书失败", "node_id", nodeID, "err", err)
		return nil, status.Error(codes.Internal, "签发隧道证书失败")
	}
	s.log.Info("节点接入成功", "node_id", nodeID, "peer", peerAddr(ctx))
	return &edgev1.EnrollResponse{
		CertPem: issued.CertPEM,
		KeyPem:  issued.KeyPEM,
		CaPem:   s.deps.CA.RootPEM(),
	}, nil
}

// Channel 是节点的长连接。身份来自客户端证书，不接受任何自报。
func (s *Server) Channel(stream edgev1.EdgeTunnel_ChannelServer) error {
	ctx := stream.Context()
	nodeID, err := identityFromPeer(ctx)
	if err != nil {
		s.log.Warn("隧道连接被拒绝", "peer", peerAddr(ctx), "err", err)
		return status.Error(codes.Unauthenticated, "缺少有效的客户端证书")
	}

	sess := &session{send: make(chan *edgev1.MasterMsg, 16)}
	s.add(nodeID, sess)
	defer s.remove(nodeID, sess)
	s.log.Info("节点已连接", "node_id", nodeID)

	errc := make(chan error, 1)
	go func() { errc <- s.recvLoop(ctx, stream, nodeID) }()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errc:
			return err
		case msg := <-sess.send:
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

func (s *Server) recvLoop(ctx context.Context, stream edgev1.EdgeTunnel_ChannelServer, nodeID string) error {
	for {
		in, err := stream.Recv()
		if err != nil {
			return err
		}
		if hb := in.GetHb(); hb != nil {
			if s.deps.Store != nil {
				if err := s.deps.Store.UpsertNodeSeen(ctx, nodeID, hb.GetCfgVersion(), time.Now()); err != nil {
					// 心跳落库失败不该断开连接：连接本身是好的，只是记录没写上。
					// 断开会让一个存储抖动升级成节点掉线。
					s.log.Error("记录心跳失败", "node_id", nodeID, "err", err)
				}
			}
		}
	}
}

// Connected 返回当前在线的节点 ID。
func (s *Server) Connected() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		out = append(out, id)
	}
	return out
}

// Send 把消息投递给指定节点。
func (s *Server) Send(nodeID string, msg *edgev1.MasterMsg) error {
	s.mu.RLock()
	sess, ok := s.sessions[nodeID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNodeNotConnected, nodeID)
	}
	select {
	case sess.send <- msg:
		return nil
	default:
		// 缓冲满说明该节点消费不过来。阻塞在这里会把一个慢节点变成主控的全局停顿。
		return fmt.Errorf("节点 %s 的发送队列已满", nodeID)
	}
}

func (s *Server) add(nodeID string, sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[nodeID] = sess
}

// remove 只在当前会话仍是登记的那一个时才删除。
//
// 节点重连时新会话会先登记、旧会话随后才收到断开并执行清理；不做这个比较的话，
// 旧连接的清理会把刚建好的新会话删掉，节点看起来在线却收不到任何下发。
func (s *Server) remove(nodeID string, sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.sessions[nodeID]; ok && cur == sess {
		delete(s.sessions, nodeID)
		s.log.Info("节点已断开", "node_id", nodeID)
	}
}

// identityFromPeer 从已验证的客户端证书里取出节点身份。
func identityFromPeer(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", errors.New("取不到对端信息")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", errors.New("连接不是 TLS")
	}
	// VerifiedChains 只在证书通过 ClientCAs 校验后才非空。用它而不是
	// PeerCertificates：后者包含对端**声称**的证书，未必通过了校验。
	if len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return "", errors.New("未出示已验证的客户端证书")
	}
	cn := tlsInfo.State.VerifiedChains[0][0].Subject.CommonName
	if cn == "" {
		return "", errors.New("客户端证书没有主体")
	}
	return cn, nil
}

func peerAddr(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		return p.Addr.String()
	}
	return "unknown"
}
