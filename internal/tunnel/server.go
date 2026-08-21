// Package tunnel 是主控侧的 gRPC 隧道端点。
//
// Agent 主动外连（穿透 NAT），一条双向流承载全部往来。首帧必须是 Hello：
// 新节点凭一次性 Token 走服务端单向 TLS，主控在这次交换里签发隧道客户端证书；
// 此后该节点全部连接走 mTLS。见 docs/adr/0009-internal-pki-two-cas.md。
package tunnel

import (
	"fmt"
	"log/slog"
	"net"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	edgev1.UnimplementedEdgeTunnelServer
	log  *slog.Logger
	grpc *grpc.Server
}

func New(log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{log: log}
	s.grpc = grpc.NewServer()
	edgev1.RegisterEdgeTunnelServer(s.grpc, s)
	return s
}

// Serve 在 addr 上开始接受连接，阻塞直到 Stop 或出错。
func (s *Server) Serve(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s: %w", addr, err)
	}
	s.log.Info("gRPC 隧道监听", "addr", addr)
	return s.grpc.Serve(lis)
}

func (s *Server) Stop() { s.grpc.GracefulStop() }

// Channel 目前只做到握手可见即止。
//
// 完整的接入、心跳、下发与回报在 issue #18 落地。这里明确返回 Unimplemented
// 而不是假装成功：一个「连上了但什么都不做」的隧道会让节点在控制台上显示为
// 在线，而它其实什么配置都收不到——那种假绿灯比连不上更难排查。
func (s *Server) Channel(stream edgev1.EdgeTunnel_ChannelServer) error {
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := msg.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "首帧必须是 Hello")
	}
	s.log.Info("收到接入握手", "node_id", hello.GetNodeId(), "agent_version", hello.GetVersion())
	return status.Error(codes.Unimplemented, "隧道尚未实现（issue #18）")
}
