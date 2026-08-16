// Package agent 是边缘节点上的常驻进程。
//
// 它不做决策，只执行与回报：接入、保持长连接、上报心跳、应用配置。
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/model"
)

const (
	// DefaultHeartbeat 是心跳周期（后端文档 §7 默认 3s）。
	DefaultHeartbeat = 3 * time.Second

	certFile = "tunnel.crt"
	keyFile  = "tunnel.key"
	caFile   = "master-ca.crt"
)

type Config struct {
	NodeID     string
	MasterAddr string // host:port
	// ServerName 用于校验主控证书。强制域名而非 IP（PRD §5 系统设置）：
	// 证书是签给域名的，用 IP 连接则无从校验主体。
	ServerName string
	// StateDir 存放隧道证书与主控 CA。
	StateDir string
	// CaddyAdmin 是本机 Caddy Admin API 地址，形如 http://127.0.0.1:2019。
	CaddyAdmin string
	// VerifyAddr 是校验端点的监听地址（只监听回环）。为空则不起。
	VerifyAddr string
	// MasterCA 是主控 CA 的根证书。
	//
	// 为空时用系统信任库——此时主控的 gRPC 端点必须持有公开可信证书。
	//
	// 【已知缺口】接入那一刻 Agent 还没有主控 CA，这是「先有鸡先有蛋」在
	// **服务端方向**的另一半，ADR-0009 只写了客户端方向。当前的解法是二选一：
	// 主控用公开可信证书（推荐，配合强制域名），或由安装脚本把 CA 根证书
	// 一并放到节点上。两者都不做的话，接入这一步就是 TOFU，中间人可在首次
	// 接入时冒充主控并骗走 Token。
	MasterCA []byte
	// MasterCAPath 是主控 CA 根证书的路径。MasterCA 非空时以它为准。
	MasterCAPath string

	HeartbeatInterval time.Duration
	Logger            *slog.Logger
}

func (c Config) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

func (c Config) heartbeat() time.Duration {
	if c.HeartbeatInterval > 0 {
		return c.HeartbeatInterval
	}
	return DefaultHeartbeat
}

func (c Config) path(name string) string { return filepath.Join(c.StateDir, name) }

// Enroll 用一次性 Token 换取隧道客户端证书并落盘。
//
// 这一步不出示客户端证书——节点此刻正是为了拿到它而来（docs/adr/0009）。
func Enroll(ctx context.Context, cfg Config, token string) error {
	if cfg.NodeID == "" {
		return fmt.Errorf("接入时缺少节点 ID")
	}
	if token == "" {
		return fmt.Errorf("接入时缺少 Token")
	}
	tlsCfg, err := cfg.serverOnlyTLS()
	if err != nil {
		return err
	}
	cc, err := grpc.NewClient(cfg.MasterAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return fmt.Errorf("连接主控: %w", err)
	}
	defer cc.Close()

	resp, err := edgev1.NewEdgeEnrollClient(cc).Enroll(ctx, &edgev1.EnrollRequest{
		NodeId: cfg.NodeID,
		Token:  token,
	})
	if err != nil {
		return fmt.Errorf("接入被拒绝: %w", err)
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return fmt.Errorf("创建状态目录: %w", err)
	}
	// 私钥 0600：同机其他用户不该能读到它。证书本身不是秘密，但一起收紧更省心。
	for name, blob := range map[string][]byte{
		certFile: resp.GetCertPem(),
		keyFile:  resp.GetKeyPem(),
		caFile:   resp.GetCaPem(),
	} {
		if len(blob) == 0 {
			return fmt.Errorf("接入响应里缺少 %s", name)
		}
		if err := os.WriteFile(cfg.path(name), blob, 0o600); err != nil {
			return fmt.Errorf("写入 %s: %w", name, err)
		}
	}
	cfg.logger().Info("接入成功", "node_id", cfg.NodeID, "state_dir", cfg.StateDir)
	return nil
}

// Run 用已落盘的隧道证书连接主控并持续上报心跳，直到 ctx 结束。
func Run(ctx context.Context, cfg Config) error {
	tlsCfg, err := cfg.mutualTLS()
	if err != nil {
		return err
	}
	cc, err := grpc.NewClient(cfg.MasterAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return fmt.Errorf("连接主控: %w", err)
	}
	defer cc.Close()

	stream, err := edgev1.NewEdgeTunnelClient(cc).Channel(ctx)
	if err != nil {
		return fmt.Errorf("建立隧道: %w", err)
	}
	// 每个 Agent 实例一份状态。日志经环形缓冲后再落到原始 handler，
	// 探活时能把「最近发生了什么」一起带回主控。
	st := newState()
	log := slog.New(&ringHandler{inner: cfg.logger().Handler(), st: st})
	log.Info("隧道已建立", "node_id", cfg.NodeID, "master", cfg.MasterAddr)

	var caddy *CaddyClient
	if cfg.CaddyAdmin != "" {
		caddy = NewCaddyClient(cfg.CaddyAdmin)
	}
	verifier := NewVerifier(log)
	if cfg.VerifyAddr != "" {
		srv, err := verifier.Serve(cfg.VerifyAddr)
		if err != nil {
			return err
		}
		defer srv.Close()
	}
	go func() {
		for {
			in, err := stream.Recv()
			if err != nil {
				return
			}
			if push := in.GetPush(); push != nil {
				handlePush(ctx, stream, caddy, verifier, push, st, log)
			}
			if probe := in.GetProbe(); probe != nil {
				handleProbe(ctx, stream, caddy, probe, st, log)
			}
		}
	}()

	ticker := time.NewTicker(cfg.heartbeat())
	defer ticker.Stop()
	for {
		if err := stream.Send(&edgev1.AgentMsg{
			M: &edgev1.AgentMsg_Hb{Hb: &edgev1.Heartbeat{
				// 心跳里没有 node_id：身份来自客户端证书，不接受自报（见 proto 注释）
				CfgVersion: st.currentCfgVersion(),
			}},
		}); err != nil {
			return fmt.Errorf("上报心跳: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// handlePush 把下发的配置应用到本机 Caddy 并回报结果。
//
// 无论成败都要回报：不回报的话主控那边只能等到超时，把一次「配置有问题」
// 报成「节点没反应」——两者的排查方向完全不同。
func handlePush(ctx context.Context, stream edgev1.EdgeTunnel_ChannelClient,
	caddy *CaddyClient, verifier *Verifier, push *edgev1.PushConfig, st *state, log *slog.Logger) {

	// 回源证书要在 Caddy 配置之前落盘：Caddy 从文件读它，配置先生效而文件
	// 还没写好的话，那一瞬所有开了回源 mTLS 的路由都会握手失败。
	if cert, key := push.GetUpstreamCert(), push.GetUpstreamKey(); len(cert) > 0 && len(key) > 0 {
		if err := writeUpstreamCert(cert, key); err != nil {
			log.Error("写入回源证书失败", "err", err)
		}
	}

	// 规则先于 Caddy 配置更新：反过来的话，Caddy 已经把请求转给校验端点，
	// 而端点手里还是旧规则——那一瞬新受保护的域名是敞开的。
	if blob := push.GetAccessRules(); len(blob) > 0 && verifier != nil {
		var rules []model.AccessRule
		if err := json.Unmarshal(blob, &rules); err != nil {
			log.Error("解析访问规则失败", "err", err)
		} else {
			verifier.SetRules(rules)
		}
	}
	res := &edgev1.PushResult{CfgVersion: push.GetCfgVersion()}
	switch {
	case caddy == nil:
		res.Ok, res.Detail = false, "本节点未配置 Caddy Admin 地址"
	default:
		applyCtx := ctx
		if ms := push.GetDeadlineMs(); ms > 0 {
			var cancel context.CancelFunc
			applyCtx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
			defer cancel()
		}
		took, err := caddy.Apply(applyCtx, push.GetCaddyJson())
		if err != nil {
			// Caddy 的原文原样回报，不做归类（docs/adr/0005）
			res.Ok, res.Detail = false, err.Error()
			log.Error("应用配置失败", "cfg_version", push.GetCfgVersion(), "err", err)
		} else {
			res.Ok, res.Detail = true, fmt.Sprintf("%dms", took)
			st.setCfgVersion(push.GetCfgVersion())
			log.Info("配置已生效", "cfg_version", push.GetCfgVersion(), "took_ms", took)
		}
	}
	if err := stream.Send(&edgev1.AgentMsg{M: &edgev1.AgentMsg_PushResult{PushResult: res}}); err != nil {
		log.Error("回报下发结果失败", "err", err)
	}
}

// handleProbe 回报节点当前状态：生效配置版本、本机 Caddy 是否可达、最近日志。
//
// 一次往返带回全部三样，而不是三个接口分别取——它们描述的是**同一时刻**的
// 节点状态，分三次取会各自看到不同的瞬间，拼出来的画面从未真实存在过。
//
// Caddy 不可达**不会**让这次回报失败：那是节点上的问题，隧道本身是好的。
// 混成一个「探活失败」会让人跑错方向——一个去查网络，一个去那台机器上
// 把 Caddy 拉起来。
func handleProbe(ctx context.Context, stream edgev1.EdgeTunnel_ChannelClient,
	caddy *CaddyClient, probe *edgev1.Probe, st *state, log *slog.Logger) {

	res := &edgev1.ProbeResult{
		Id:         probe.GetId(),
		CfgVersion: st.currentCfgVersion(),
	}
	switch {
	case caddy == nil:
		res.CaddyOk, res.CaddyDetail = false, "本节点未配置 Caddy Admin 地址"
	default:
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := caddy.Ping(pingCtx)
		cancel()
		if err != nil {
			res.CaddyOk, res.CaddyDetail = false, err.Error()
		} else {
			res.CaddyOk, res.CaddyDetail = true, "Admin API 正常应答"
		}
	}
	// 日志在最后取：上面那次 Ping 失败的记录也该被带回去
	res.Logs = st.recentLogs()

	if err := stream.Send(&edgev1.AgentMsg{M: &edgev1.AgentMsg_ProbeResult{ProbeResult: res}}); err != nil {
		log.Error("回报探活结果失败", "err", err)
	}
}

func (c Config) rootPool() (*x509.CertPool, error) {
	ca := c.MasterCA
	if len(ca) == 0 && c.MasterCAPath != "" {
		blob, err := os.ReadFile(c.MasterCAPath)
		if err != nil {
			// 明确指定了路径却读不到，是配置错误，不能悄悄退回系统信任库——
			// 那会让「主控证书由谁签」这件事在无人察觉的情况下换掉
			return nil, fmt.Errorf("读取主控 CA %s: %w", c.MasterCAPath, err)
		}
		ca = blob
	}
	if len(ca) == 0 {
		// 落盘的 CA（接入时保存的）优先于系统信任库
		if blob, err := os.ReadFile(c.path(caFile)); err == nil {
			ca = blob
		}
	}
	if len(ca) == 0 {
		return nil, nil // nil 表示用系统信任库
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("主控 CA 不是合法的 PEM")
	}
	return pool, nil
}

func (c Config) serverOnlyTLS() (*tls.Config, error) {
	pool, err := c.rootPool()
	if err != nil {
		return nil, err
	}
	return &tls.Config{RootCAs: pool, ServerName: c.ServerName, MinVersion: tls.VersionTLS12}, nil
}

func (c Config) mutualTLS() (*tls.Config, error) {
	cfg, err := c.serverOnlyTLS()
	if err != nil {
		return nil, err
	}
	pair, err := tls.LoadX509KeyPair(c.path(certFile), c.path(keyFile))
	if err != nil {
		return nil, fmt.Errorf("加载隧道证书（是否还没接入？）: %w", err)
	}
	cfg.Certificates = []tls.Certificate{pair}
	return cfg, nil
}
