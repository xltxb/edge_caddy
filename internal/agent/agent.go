// Package agent 是边缘节点上的常驻进程。
//
// 它不做决策，只执行与回报：接入、保持长连接、上报心跳、应用配置。
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
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
	log := cfg.logger()
	log.Info("隧道已建立", "node_id", cfg.NodeID, "master", cfg.MasterAddr)

	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				return
			}
			// 首切片只需保活；下发消息的处理在 issue #2
		}
	}()

	ticker := time.NewTicker(cfg.heartbeat())
	defer ticker.Stop()
	for {
		if err := stream.Send(&edgev1.AgentMsg{
			M: &edgev1.AgentMsg_Hb{Hb: &edgev1.Heartbeat{
				// 心跳里没有 node_id：身份来自客户端证书，不接受自报（见 proto 注释）
				CfgVersion: currentCfgVersion(),
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

// currentCfgVersion 返回节点当前生效的配置版本。
// 首切片尚未应用过任何配置，返回空串；issue #2 接上真实版本。
func currentCfgVersion() string { return "" }

func (c Config) rootPool() (*x509.CertPool, error) {
	ca := c.MasterCA
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
