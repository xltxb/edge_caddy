package tunnel

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
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

	// geoPushing 挡住重复推送。心跳每几秒一次，而推一份库要几秒 ——
	// 不挡的话一台落后的节点会被同一份库连推十几次，把隧道占满。
	geoPushing atomic.Bool
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
			if err := srv.opt.Store.TouchHeartbeatWithGeo(ctx, s.nodeID, hb.GetCfgVersion(), status, hb.GetGeoDbSha()); err != nil {
				srv.log.Error("记录心跳失败", "node_id", s.nodeID, "err", err)
			}

			// **GeoIP 库靠心跳比对推送，不搭配置下发的车。**
			//
			// 搭车的话，一台配置从没变过的节点永远拿不到库，
			// 而它上面的地域规则一直不生效 —— 而界面上那条规则显示为启用。
			//
			// 判据是节点报的那个哈希，不是主控记的「我推过什么」：
			// 主控记账的话，一次推送失败之后它会一直以为节点有库。
			srv.maybePushGeoDB(ctx, s, hb.GetGeoDbSha())

		case *edgev1.AgentMsg_PushResult:
			s.deliver(m.PushResult)

		case *edgev1.AgentMsg_ProbeResult:
			s.deliverProbe(m.ProbeResult)

		case *edgev1.AgentMsg_DrainResult:
			s.deliverDrain(m.DrainResult)

		case *edgev1.AgentMsg_Logs:
			lines := make([]store.NodeLogLine, 0, len(m.Logs.GetLines()))
			for _, l := range m.Logs.GetLines() {
				lines = append(lines, store.NodeLogLine{
					At:    time.UnixMilli(l.GetAtUnixMs()),
					Level: l.GetLevel(),
					Msg:   l.GetMsg(),
				})
			}
			if err := srv.opt.Store.AppendNodeLogs(ctx, s.nodeID, lines); err != nil {
				srv.log.Error("保存节点日志失败", "node_id", s.nodeID, "err", err)
			}

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
			// 现在每一种 AgentMsg 都有人接了。
			//
			// 这条 default 分支的注释我改过三次，两次是错的：第一版列举式
			// 「日志、证书清单、探活回执在后续工单落地」，而后两样早就在上面
			// 处理掉了——**一条列举式的欠条，兑现一项就假一分**，
			// 而它读起来始终完整。第二版「节点日志从 GET /nodes/:id/logs
			// 那一侧走」是**假话**：我没去查那个端点在不在，而它在契约里
			// 格式完整，看着就像在。
			//
			// 留着这条注释是因为它记着一件事：**一个诚实的欠条被换成一句
			// 自信的假话，比原来更糟——前一句会让人去查，后一句会让人放心。**
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

// maybePushGeoDB 在节点的库与主控那份不一致时推一份过去。
//
// **失败只记日志，不影响心跳。** 库推不过去时地域规则不生效，
// 而那件事由 Agent 那一侧说出来（它每次放行都会写一条 warn）——
// 在这里把心跳处理搞失败，会让一个节点因为一个附加功能而显示成异常。
func (srv *Server) maybePushGeoDB(ctx context.Context, s *session, nodeSHA string) {
	if srv.opt.Store == nil {
		return
	}
	cur, err := srv.opt.Store.GetGeoDB(ctx, false)
	if err != nil {
		return // 主控自己都没有库，没什么可推的
	}
	if cur.SHA256 == "" || cur.SHA256 == nodeSHA {
		return
	}
	// **一个节点同时只推一次。** 心跳每几秒一次，而推一次要几秒——
	// 不挡的话一台落后的节点会被同一份库连推十几次，
	// 每次几 MB，把隧道占满。
	if !s.geoPushing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.geoPushing.Store(false)
		full, err := srv.opt.Store.GetGeoDB(context.WithoutCancel(ctx), true)
		if err != nil {
			srv.log.Error("读取 GeoIP 库失败", "err", err)
			return
		}
		gz, err := gzipBytes(full.MMDB)
		if err != nil {
			srv.log.Error("压缩 GeoIP 库失败", "err", err)
			return
		}
		srv.log.Info("推送 GeoIP 库", "node_id", s.nodeID,
			"sha256", full.SHA256[:12], "压缩后字节", len(gz))
		msg := &edgev1.MasterMsg{M: &edgev1.MasterMsg_GeoDb{
			GeoDb: &edgev1.PushGeoDB{MmdbGz: gz, Sha256: full.SHA256},
		}}
		select {
		case s.out <- msg:
		case <-s.closed:
			srv.log.Warn("推送 GeoIP 库时隧道已断", "node_id", s.nodeID)
		case <-time.After(30 * time.Second):
			// **超时要说出来。** 悄悄丢掉的话，节点会一直报旧哈希、
			// 主控会一直重推，而两边都不知道为什么推不动。
			srv.log.Error("推送 GeoIP 库超时，发送队列满", "node_id", s.nodeID)
		}
	}()
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
