package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	certFile = "tunnel.crt"
	keyFile  = "tunnel.key"
	caFile   = "tunnel-ca.crt"
)

type Config struct {
	MasterAddr string
	NodeID     string
	Token      string // 仅首次接入需要
	CAPin      string // 主控隧道 CA 的 SHA-256，十六进制
	StateDir   string
	CaddyAdmin string
	// VerifyListen 是校验端点的监听地址：host:port 或 unix/<绝对路径>。
	VerifyListen string
	// TLSProbe 是本机 TLS server 的地址，用来探证书回执。空则默认 127.0.0.1:443。
	TLSProbe  string
	Heartbeat time.Duration
	Log       *slog.Logger
	Version   string
}

type Agent struct {
	cfg     Config
	log     *slog.Logger
	caddy   *CaddyClient
	verify  *VerifyServer
	metrics *metricsCollector

	mu         sync.Mutex
	cfgVersion string
	routes     uint32
	rules      uint32
}

func New(cfg Config) *Agent {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Heartbeat == 0 {
		cfg.Heartbeat = 3 * time.Second
	}
	caddy := NewCaddyClient(cfg.CaddyAdmin)
	return &Agent{
		cfg: cfg, log: cfg.Log,
		caddy:   caddy,
		verify:  NewVerifyServer(cfg.Log),
		metrics: newMetricsCollector(caddy),
	}
}

// Verify 暴露校验端点，供调用方挂到监听上。
func (a *Agent) Verify() *VerifyServer { return a.verify }

// Run 建立一条隧道并跑到它断开为止。重连由调用方负责。
func (a *Agent) Run(ctx context.Context) error {
	creds, enrolling, err := a.credentials()
	if err != nil {
		return err
	}

	conn, err := grpc.NewClient(a.cfg.MasterAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("连接主控: %w", err)
	}
	defer conn.Close()

	stream, err := edgev1.NewEdgeTunnelClient(conn).Channel(ctx)
	if err != nil {
		return fmt.Errorf("打开隧道: %w", err)
	}

	hello := &edgev1.Hello{NodeId: a.cfg.NodeID, Version: a.cfg.Version}
	if enrolling {
		hello.Token = a.cfg.Token
	}
	if err := stream.Send(&edgev1.AgentMsg{M: &edgev1.AgentMsg_Hello{Hello: hello}}); err != nil {
		return fmt.Errorf("发送 Hello: %w", err)
	}

	first, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("等待主控应答: %w", err)
	}
	enrolled := first.GetEnrolled()
	if enrolled == nil {
		return errors.New("主控的首帧应答不是 Enrolled")
	}
	if len(enrolled.GetTunnelCertPem()) > 0 {
		if err := a.saveIdentity(enrolled); err != nil {
			return fmt.Errorf("保存隧道证书: %w", err)
		}
		a.log.Info("接入完成，已取得隧道证书", "node_id", a.cfg.NodeID)
	}
	a.mu.Lock()
	a.cfgVersion = enrolled.GetCfgVersion()
	a.mu.Unlock()

	go a.heartbeatLoop(ctx, stream)

	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		switch m := msg.M.(type) {
		case *edgev1.MasterMsg_Push:
			a.handlePush(ctx, stream, m.Push)
		case *edgev1.MasterMsg_Probe:
			a.handleProbe(ctx, stream, m.Probe)
		case *edgev1.MasterMsg_Drain:
			// 排空要等，不能占着这条读循环 —— 占住的话主控这段时间
			// 推不下来配置也探不了活，而排空可能要等几十秒。
			go a.handleDrain(ctx, stream, m.Drain)
		default:
			// 这里原先写着「证书续期在后续工单落地」。**那句话是假的，
			// 而且是最坏的一种假：它承诺了一件按现在的架构不该发生的事。**
			//
			// 证书随每次下发内联带上（ADR-0010），主控续期之后触发一次下发即可，
			// 不需要给 Agent 发单独的续期指令。谁照着那句话去实现 RenewCert，
			// 会做出一条与 ADR-0010 冲突的下发路径。
			//
			// proto 里那条消息已经删掉并 reserved。
		}
	}
}

func (a *Agent) handlePush(ctx context.Context, stream edgev1.EdgeTunnel_ChannelClient, p *edgev1.PushConfig) {
	deadline := time.Duration(p.GetDeadlineMs()) * time.Millisecond
	if deadline <= 0 {
		deadline = 5 * time.Second
	}
	applyCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	// 先装规则再应用配置。反过来的话，配置生效的那一瞬间校验端点还不认识
	// 新规则，那几毫秒里受保护域名会整体 403 —— 一次成功的下发不该有
	// 任何一刻是拒绝服务的。
	if raw := p.GetVerifyRules(); len(raw) > 0 {
		var rules []model.VerifyRule
		if err := json.Unmarshal(raw, &rules); err != nil {
			a.log.Error("解析校验规则失败", "err", err)
		} else {
			a.verify.SetRules(rules)
		}
	} else {
		a.verify.SetRules(nil)
	}

	// 主控渲染的校验端点地址与本机监听的必须一致。不一致就拒绝这份配置——
	// 应用下去的话每个受保护域名都会 502，而配置本身看起来完全正常。
	if err := CheckVerifyAddr(p.GetCaddyJson(), a.cfg.VerifyListen); err != nil {
		a.log.Error("校验端点地址不一致", "err", err)
		res := &edgev1.PushResult{CfgVersion: p.GetCfgVersion(), Ok: false, Detail: err.Error()}
		if sendErr := stream.Send(&edgev1.AgentMsg{M: &edgev1.AgentMsg_PushResult{PushResult: res}}); sendErr != nil {
			a.log.Error("回报下发结果失败", "err", sendErr)
		}
		return
	}

	// **回源证书要先落盘再应用配置。** 反过来的话，配置里引用的文件那一刻
	// 还不存在，Caddy 会整份拒绝——而报错是「文件不存在」，跟证书轮换
	// 看起来毫无关系。
	if err := writeUpstreamCert(p); err != nil {
		a.log.Error("写入回源证书失败", "err", err)
		res := &edgev1.PushResult{CfgVersion: p.GetCfgVersion(), Ok: false, Detail: err.Error()}
		if sendErr := stream.Send(&edgev1.AgentMsg{M: &edgev1.AgentMsg_PushResult{PushResult: res}}); sendErr != nil {
			a.log.Error("回报下发结果失败", "err", sendErr)
		}
		return
	}

	took, err := a.caddy.ApplyConfig(applyCtx, p.GetCaddyJson())

	res := &edgev1.PushResult{CfgVersion: p.GetCfgVersion()}
	if err != nil {
		// 原文回报，不做归类。主控靠「有没有收到这条消息」区分传输层失败与
		// Caddy 拒绝，不靠 detail 的措辞（ADR-0005）。
		res.Ok, res.Detail = false, err.Error()
		a.log.Warn("应用配置失败", "cfg_version", p.GetCfgVersion(), "err", err)
	} else {
		res.Ok = true
		res.Detail = fmt.Sprintf("%dms", took.Milliseconds())
		go a.reportCerts(context.WithoutCancel(ctx), stream, p.GetCaddyJson())
		a.mu.Lock()
		a.cfgVersion = p.GetCfgVersion()
		a.routes, a.rules = p.GetRoutes(), p.GetRules()
		a.mu.Unlock()
		a.metrics.setEdgePorts(edgePorts(p.GetCaddyJson()))
		a.log.Info("配置已应用", "cfg_version", p.GetCfgVersion(), "took", res.Detail)
	}

	if err := stream.Send(&edgev1.AgentMsg{M: &edgev1.AgentMsg_PushResult{PushResult: res}}); err != nil {
		a.log.Error("回报下发结果失败", "err", err)
	}
}

// handleProbe 回一次探活。caddy_admin 是**本机 Caddy** 的可达性，
// 与隧道可达性分开报：隧道通而 Admin 不通说明 Caddy 挂了而 Agent 还活着，
// 这两种故障的处置完全不同。
func (a *Agent) handleProbe(ctx context.Context, stream edgev1.EdgeTunnel_ChannelClient, p *edgev1.Probe) {
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	a.mu.Lock()
	ver := a.cfgVersion
	a.mu.Unlock()

	res := &edgev1.ProbeResult{
		Id:         p.GetId(),
		CaddyAdmin: a.caddy.Alive(pingCtx),
		CfgVersion: ver,
	}
	if err := stream.Send(&edgev1.AgentMsg{M: &edgev1.AgentMsg_ProbeResult{ProbeResult: res}}); err != nil {
		a.log.Error("回报探活失败", "err", err)
	}
}

// writeUpstreamCert 把主控下发的回源客户端证书落盘。
//
// 路径来自主控（见 proto 里 PushConfig 的说明）：Caddy 的
// client_certificate_file 不接受内联，若让两边各自决定路径，那就是两个进程
// 持有同一份知识，迟早会不一致。
func writeUpstreamCert(p *edgev1.PushConfig) error {
	certPEM, keyPEM := p.GetUpstreamCertPem(), p.GetUpstreamKeyPem()
	certPath, keyPath := p.GetUpstreamCertPath(), p.GetUpstreamKeyPath()
	if len(certPEM) == 0 || certPath == "" {
		return nil // 这次下发没有带回源证书
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return fmt.Errorf("建立证书目录: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("写入回源证书: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("写入回源私钥: %w", err)
	}
	return nil
}

// reportCerts 在配置应用之后回报**实际出示的**证书。
//
// 契约要回答的是「下发到了之后节点有没有真的加载」，所以这里去回环上真握一次手
// 读对端的证书，而不是复述主控下发的那份——后者只能证明「我收到了这些 PEM」。
// 两者的区别正是 ADR-0004 复核时那个「幽灵监听」教过的：配置被接受不等于在服务。
func (a *Agent) reportCerts(ctx context.Context, stream edgev1.EdgeTunnel_ChannelClient, caddyJSON []byte) {
	domains := certDomainsOf(caddyJSON)
	if len(domains) == 0 {
		// 没有内联证书就报一份空清单——**必须报**，否则节点上刚被撤掉的证书
		// 会在主控这边一直显示为「已加载」。
		if err := stream.Send(&edgev1.AgentMsg{
			M: &edgev1.AgentMsg_Certs{Certs: &edgev1.CertList{}},
		}); err != nil {
			a.log.Debug("回报空证书清单失败", "err", err)
		}
		return
	}

	// 配置刚 POST 下去，TLS app 装载需要一点时间。给它几次机会，
	// 而不是立刻断言「没加载」。
	tlsAddr := a.cfg.TLSProbe
	if tlsAddr == "" {
		tlsAddr = "127.0.0.1:443"
	}
	var receipts []certReceipt
	for i := 0; i < 5; i++ {
		receipts = collectCertReceipts(ctx, tlsAddr, domains, a.log)
		if len(receipts) == len(domains) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err := stream.Send(&edgev1.AgentMsg{
		M: &edgev1.AgentMsg_Certs{Certs: toProtoCerts(receipts)},
	}); err != nil {
		a.log.Debug("回报证书清单失败", "err", err)
	}
}

func (a *Agent) heartbeatLoop(ctx context.Context, stream edgev1.EdgeTunnel_ChannelClient) {
	t := time.NewTicker(a.cfg.Heartbeat)
	defer t.Stop()
	send := func() {
		m := a.metrics.collect(ctx)
		a.mu.Lock()
		hb := &edgev1.Heartbeat{
			NodeId: a.cfg.NodeID, CfgVersion: a.cfgVersion,
			Cpu: m.CPU, Mem: m.Mem, Conns: m.Conns,
			Routes: a.routes, Rules: a.rules,
			ReqTotal: m.ReqTotal, OriginTotal: m.OriginTotal,
		}
		a.mu.Unlock()
		if err := stream.Send(&edgev1.AgentMsg{M: &edgev1.AgentMsg_Hb{Hb: hb}}); err != nil {
			a.log.Debug("心跳发送失败（隧道多半已断）", "err", err)
		}
	}
	send() // 立刻发一次：否则控制台要等一个周期才看到这台机器
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			send()
		}
	}
}

// credentials 决定这次连接的身份。
//
// 已经落盘过隧道证书就走 mTLS；否则走服务端单向 TLS + 一次性 Token，
// 并用安装命令带来的指纹校验主控（ADR-0009）。
func (a *Agent) credentials() (credentials.TransportCredentials, bool, error) {
	certPath := filepath.Join(a.cfg.StateDir, certFile)
	keyPath := filepath.Join(a.cfg.StateDir, keyFile)
	caPath := filepath.Join(a.cfg.StateDir, caFile)

	if fileExists(certPath) && fileExists(keyPath) && fileExists(caPath) {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, false, fmt.Errorf("加载隧道证书: %w", err)
		}
		caPEM, err := os.ReadFile(caPath)
		if err != nil {
			return nil, false, err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(caPEM) {
			return nil, false, errors.New("本地隧道 CA 无法加入信任池")
		}
		return credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{cert},
			RootCAs:      roots,
			MinVersion:   tls.VersionTLS12,
		}), false, nil
	}

	if a.cfg.Token == "" {
		return nil, false, errors.New("本地没有隧道证书，也没有接入 Token")
	}
	if a.cfg.CAPin == "" {
		return nil, false, errors.New("首次接入必须提供 --ca-pin：否则无法确认对面就是主控")
	}
	return credentials.NewTLS(&tls.Config{
		// 自己验：链里必须出现一张指纹与 --ca-pin 相符的 CA，且叶子由它签出。
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: pinnedVerifier(a.cfg.CAPin),
		MinVersion:            tls.VersionTLS12,
	}), true, nil
}

func pinnedVerifier(pin string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("主控没有出示任何证书")
		}
		var pinned *x509.Certificate
		for _, raw := range rawCerts {
			c, err := x509.ParseCertificate(raw)
			if err != nil {
				return fmt.Errorf("解析主控证书: %w", err)
			}
			sum := sha256.Sum256(c.Raw)
			if hex.EncodeToString(sum[:]) == pin {
				pinned = c
				break
			}
		}
		if pinned == nil {
			return errors.New("主控出示的证书链里没有与 --ca-pin 相符的 CA —— 对面可能不是你的主控")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return err
		}
		roots := x509.NewCertPool()
		roots.AddCert(pinned)
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots:     roots,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			return fmt.Errorf("主控证书不由被固定的 CA 签出: %w", err)
		}
		return nil
	}
}

func (a *Agent) saveIdentity(e *edgev1.Enrolled) error {
	if err := os.MkdirAll(a.cfg.StateDir, 0o700); err != nil {
		return err
	}
	files := map[string][]byte{
		certFile: e.GetTunnelCertPem(),
		keyFile:  e.GetTunnelKeyPem(),
		caFile:   e.GetTunnelCaPem(),
	}
	for name, body := range files {
		mode := os.FileMode(0o644)
		if name == keyFile {
			mode = 0o600
		}
		if err := os.WriteFile(filepath.Join(a.cfg.StateDir, name), body, mode); err != nil {
			return err
		}
	}
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// edgePorts 从下发的配置里读出边缘 server 监听的端口，供连接数统计使用。
// 不限定端口的话，SSH 与隧道自己的连接都会被算进「连接数」，那个数字就没有意义。
// unix socket 上的监听没有端口，自然不计入。
func edgePorts(caddyJSON []byte) []uint32 {
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen []string `json:"listen"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if json.Unmarshal(caddyJSON, &cfg) != nil {
		return nil
	}
	var out []uint32
	for _, srv := range cfg.Apps.HTTP.Servers {
		for _, l := range srv.Listen {
			_, port, found := strings.Cut(l, ":")
			if !found {
				continue
			}
			if n, err := strconv.ParseUint(port, 10, 32); err == nil {
				out = append(out, uint32(n))
			}
		}
	}
	return out
}
