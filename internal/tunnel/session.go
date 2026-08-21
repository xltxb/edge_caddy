package tunnel

import (
	"context"
	"fmt"
	"sync"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
)

// session 是一条活着的隧道。
//
// 写入集中在 writeLoop 一个 goroutine 里：gRPC 的流不允许并发 Send，
// 而下发、探活、续期都可能同时想往下写。
type session struct {
	nodeID string
	stream edgev1.EdgeTunnel_ChannelServer

	out    chan *edgev1.MasterMsg
	closed chan struct{}
	once   sync.Once

	mu       sync.Mutex
	waiters  map[string]chan *edgev1.PushResult // key 是 cfg_version
	probes   map[string]chan *edgev1.ProbeResult
	probeSeq int
}

func newSession(nodeID string, stream edgev1.EdgeTunnel_ChannelServer) *session {
	return &session{
		nodeID:  nodeID,
		stream:  stream,
		out:     make(chan *edgev1.MasterMsg, 8),
		closed:  make(chan struct{}),
		waiters: map[string]chan *edgev1.PushResult{},
		probes:  map[string]chan *edgev1.ProbeResult{},
	}
}

func (s *session) close() {
	s.once.Do(func() { close(s.closed) })
}

func (s *session) writeLoop() {
	for {
		select {
		case <-s.closed:
			return
		case msg := <-s.out:
			if err := s.stream.Send(msg); err != nil {
				s.close()
				return
			}
		}
	}
}

func (s *session) readLoop(ctx context.Context, srv *Server) error {
	for {
		msg, err := s.stream.Recv()
		if err != nil {
			s.close()
			return err
		}

		switch m := msg.M.(type) {
		case *edgev1.AgentMsg_Hb:
			hb := m.Hb
			if err := srv.opt.Store.TouchHeartbeat(ctx, s.nodeID, hb.GetCfgVersion()); err != nil {
				srv.log.Error("记录心跳失败", "node_id", s.nodeID, "err", err)
			}
			if srv.opt.OnHeartbeat != nil {
				srv.opt.OnHeartbeat(Heartbeat{
					NodeID: s.nodeID, CPU: hb.GetCpu(), Mem: hb.GetMem(),
					Conns: hb.GetConns(), CfgVersion: hb.GetCfgVersion(),
					Routes: hb.GetRoutes(), Rules: hb.GetRules(),
					ReqTotal: hb.GetReqTotal(), OriginTotal: hb.GetOriginTotal(),
				})
			}

		case *edgev1.AgentMsg_PushResult:
			s.deliver(m.PushResult)

		case *edgev1.AgentMsg_ProbeResult:
			s.deliverProbe(m.ProbeResult)

		case *edgev1.AgentMsg_Hello:
			// 重复的 Hello。不是错误，忽略即可——Agent 重连时可能补发。

		default:
			// 其余消息类型在后续工单落地（日志、证书清单、探活回执）。
		}

		select {
		case <-s.closed:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

func (s *session) deliver(r *edgev1.PushResult) {
	s.mu.Lock()
	ch := s.waiters[r.GetCfgVersion()]
	delete(s.waiters, r.GetCfgVersion())
	s.mu.Unlock()
	if ch != nil {
		ch <- r
	}
}

// push 下发一份配置并等回报。
func (s *session) push(ctx context.Context, cfgVersion string, caddyJSON, verifyRules []byte, counts ResourceCounts, deadline time.Duration) PushOutcome {
	ch := make(chan *edgev1.PushResult, 1)
	s.mu.Lock()
	s.waiters[cfgVersion] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiters, cfgVersion)
		s.mu.Unlock()
	}()

	msg := &edgev1.MasterMsg{M: &edgev1.MasterMsg_Push{Push: &edgev1.PushConfig{
		CfgVersion: cfgVersion, CaddyJson: caddyJSON, VerifyRules: verifyRules,
		Routes: counts.Routes, Rules: counts.Rules,
		DeadlineMs: uint32(deadline.Milliseconds()),
	}}}

	select {
	case s.out <- msg:
	case <-s.closed:
		return PushOutcome{Detail: "隧道已断开", Responded: false}
	case <-ctx.Done():
		return PushOutcome{Detail: "已取消", Responded: false}
	}

	timer := time.NewTimer(deadline)
	defer timer.Stop()

	select {
	case r := <-ch:
		// 节点回应了。ok=false 意味着 Caddy 拒绝了这份配置——不重试。
		return PushOutcome{OK: r.GetOk(), Detail: r.GetDetail(), Responded: true}
	case <-timer.C:
		// 节点没回应。这是传输层失败，重试对它有意义。
		return PushOutcome{Detail: "deadline exceeded", Responded: false}
	case <-s.closed:
		return PushOutcome{Detail: "隧道已断开", Responded: false}
	case <-ctx.Done():
		return PushOutcome{Detail: "已取消", Responded: false}
	}
}

func (s *session) deliverProbe(r *edgev1.ProbeResult) {
	s.mu.Lock()
	ch := s.probes[r.GetId()]
	delete(s.probes, r.GetId())
	s.mu.Unlock()
	if ch != nil {
		ch <- r
	}
}

// probe 在隧道上真往返一次，测的是**这条隧道**通不通，
// 而不是「主控这边的会话表里还有这一行」。会话表里有而对端已经死掉，
// 是网络里最常见的一种状态。
func (s *session) probe(ctx context.Context, timeout time.Duration) (ProbeOutcome, error) {
	s.mu.Lock()
	s.probeSeq++
	id := fmt.Sprintf("p%d", s.probeSeq)
	ch := make(chan *edgev1.ProbeResult, 1)
	s.probes[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.probes, id)
		s.mu.Unlock()
	}()

	start := time.Now()
	msg := &edgev1.MasterMsg{M: &edgev1.MasterMsg_Probe{Probe: &edgev1.Probe{Id: id}}}
	select {
	case s.out <- msg:
	case <-s.closed:
		return ProbeOutcome{}, errUnreachable
	case <-ctx.Done():
		return ProbeOutcome{}, ctx.Err()
	}

	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return ProbeOutcome{
			RTT:        time.Since(start),
			CaddyAdmin: r.GetCaddyAdmin(),
			CfgVersion: r.GetCfgVersion(),
		}, nil
	case <-t.C:
		return ProbeOutcome{}, errUnreachable
	case <-s.closed:
		return ProbeOutcome{}, errUnreachable
	case <-ctx.Done():
		return ProbeOutcome{}, ctx.Err()
	}
}
