package tunnel

import (
	"context"
	"fmt"
	"sync"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/store"
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
	drains   map[string]chan *edgev1.DrainResult
	drainSeq int
}

func newSession(nodeID string, stream edgev1.EdgeTunnel_ChannelServer) *session {
	return &session{
		nodeID:  nodeID,
		stream:  stream,
		out:     make(chan *edgev1.MasterMsg, 8),
		closed:  make(chan struct{}),
		waiters: map[string]chan *edgev1.PushResult{},
		probes:  map[string]chan *edgev1.ProbeResult{},
		drains:  map[string]chan *edgev1.DrainResult{},
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
			beat := Heartbeat{
				NodeID: s.nodeID, CPU: hb.GetCpu(), Mem: hb.GetMem(),
				Conns: hb.GetConns(), CfgVersion: hb.GetCfgVersion(),
				Routes: hb.GetRoutes(), Rules: hb.GetRules(),
				ReqTotal: hb.GetReqTotal(), OriginTotal: hb.GetOriginTotal(),
			}
			// 健康分档由 OnHeartbeat 那一侧给出——判断标准（阈值）在那里，
			// 隧道只负责把心跳原样送过去。
			status := "ok"
			if srv.opt.OnHeartbeat != nil {
				status = srv.opt.OnHeartbeat(beat)
			}
			if err := srv.opt.Store.TouchHeartbeat(ctx, s.nodeID, hb.GetCfgVersion(), status); err != nil {
				srv.log.Error("记录心跳失败", "node_id", s.nodeID, "err", err)
			}

		case *edgev1.AgentMsg_PushResult:
			s.deliver(m.PushResult)

		case *edgev1.AgentMsg_ProbeResult:
			s.deliverProbe(m.ProbeResult)

		case *edgev1.AgentMsg_DrainResult:
			s.deliverDrain(m.DrainResult)

		case *edgev1.AgentMsg_Certs:
			// 回执**整体替换**：一张已经从节点上消失的证书，旧回执留着会让
			// 证书页一直显示「这台机器加载了」——而实际上没有。
			var receipts []store.CertNode
			for _, e := range m.Certs.GetEntries() {
				receipts = append(receipts, store.CertNode{
					Domain:      e.GetDomain(),
					NodeID:      s.nodeID,
					NotAfter:    time.Unix(e.GetNotAfterUnix(), 0),
					Fingerprint: e.GetFingerprint(),
				})
			}
			if err := srv.opt.Store.ReplaceCertReceipts(ctx, s.nodeID, receipts); err != nil {
				srv.log.Error("保存证书回执失败", "node_id", s.nodeID, "err", err)
			}

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
func (s *session) push(ctx context.Context, cfgVersion string, caddyJSON, verifyRules []byte, counts ResourceCounts, up UpstreamCert, deadline time.Duration) PushOutcome {
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
		UpstreamCertPem:  up.CertPEM,
		UpstreamKeyPem:   up.KeyPEM,
		UpstreamCertPath: up.CertPath,
		UpstreamKeyPath:  up.KeyPath,
		DeadlineMs:       uint32(deadline.Milliseconds()),
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

func (s *session) deliverDrain(r *edgev1.DrainResult) {
	s.mu.Lock()
	ch := s.drains[r.GetId()]
	delete(s.drains, r.GetId())
	s.mu.Unlock()
	if ch != nil {
		ch <- r
	}
}

// drain 让节点等已建立的连接结束，回报还剩多少。
//
// **超时比 Agent 那边宽一点。** 两边卡同一个数的话，Agent 到点回报的那一刻
// 主控可能已经放弃了，于是一个如实的回执被当成「节点没应答」——
// 而那正好是排空最有话要说的时候（它就是要告诉你还剩几条）。
func (s *session) drain(ctx context.Context, timeout time.Duration) (DrainOutcome, error) {
	s.mu.Lock()
	s.drainSeq++
	id := fmt.Sprintf("d%d", s.drainSeq)
	ch := make(chan *edgev1.DrainResult, 1)
	s.drains[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.drains, id)
		s.mu.Unlock()
	}()

	msg := &edgev1.MasterMsg{M: &edgev1.MasterMsg_Drain{Drain: &edgev1.Drain{
		Id: id, TimeoutMs: uint32(timeout.Milliseconds()),
	}}}
	select {
	case s.out <- msg:
	case <-s.closed:
		return DrainOutcome{}, errUnreachable
	case <-ctx.Done():
		return DrainOutcome{}, ctx.Err()
	}

	t := time.NewTimer(timeout + drainGrace)
	defer t.Stop()
	select {
	case r := <-ch:
		return DrainOutcome{Drained: r.GetDrained(), Remaining: r.GetRemaining()}, nil
	case <-t.C:
		return DrainOutcome{}, errUnreachable
	case <-s.closed:
		return DrainOutcome{}, errUnreachable
	case <-ctx.Done():
		return DrainOutcome{}, ctx.Err()
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
