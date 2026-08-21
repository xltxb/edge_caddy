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
	Heartbeat    time.Duration
	Log          *slog.Logger
	Version      string
}

type Agent struct {
	cfg    Config
	log    *slog.Logger
	caddy  *CaddyClient
	verify *VerifyServer

	cfgVersion string
}

func New(cfg Config) *Agent {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Heartbeat == 0 {
		cfg.Heartbeat = 3 * time.Second
	}
	return &Agent{
		cfg: cfg, log: cfg.Log,
		caddy:  NewCaddyClient(cfg.CaddyAdmin),
		verify: NewVerifyServer(cfg.Log),
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
	a.cfgVersion = enrolled.GetCfgVersion()

	go a.heartbeatLoop(ctx, stream)

	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		switch m := msg.M.(type) {
		case *edgev1.MasterMsg_Push:
			a.handlePush(ctx, stream, m.Push)
		default:
			// 探活、下线、续期在后续工单落地。
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
		a.cfgVersion = p.GetCfgVersion()
		a.log.Info("配置已应用", "cfg_version", p.GetCfgVersion(), "took", res.Detail)
	}

	if err := stream.Send(&edgev1.AgentMsg{M: &edgev1.AgentMsg_PushResult{PushResult: res}}); err != nil {
		a.log.Error("回报下发结果失败", "err", err)
	}
}

func (a *Agent) heartbeatLoop(ctx context.Context, stream edgev1.EdgeTunnel_ChannelClient) {
	t := time.NewTicker(a.cfg.Heartbeat)
	defer t.Stop()
	send := func() {
		err := stream.Send(&edgev1.AgentMsg{M: &edgev1.AgentMsg_Hb{Hb: &edgev1.Heartbeat{
			NodeId: a.cfg.NodeID, CfgVersion: a.cfgVersion,
		}}})
		if err != nil {
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
