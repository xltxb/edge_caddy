// agent 跑在边缘节点上：一条到主控的 gRPC 长连接，加上对本机 Caddy 的托管。
//
// 它不做决策，只执行与回报（CONTEXT.md「Agent」）。
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xltxb/edge_caddy/internal/agent"
)

func main() {
	var cfg agent.Config
	flag.StringVar(&cfg.MasterAddr, "master", env("EC_MASTER_ADDR", ""), "主控隧道地址 host:port")
	flag.StringVar(&cfg.NodeID, "node-id", env("EC_NODE_ID", ""), "节点标识")
	flag.StringVar(&cfg.Token, "token", env("EC_ENROLL_TOKEN", ""), "一次性接入 Token（仅首次接入需要）")
	flag.StringVar(&cfg.CAPin, "ca-pin", env("EC_CA_PIN", ""), "主控隧道 CA 的 SHA-256 指纹（首次接入必需）")
	flag.StringVar(&cfg.StateDir, "state-dir", env("EC_STATE_DIR", "/var/lib/edge-agent"), "隧道证书存放目录")
	flag.StringVar(&cfg.CaddyAdmin, "caddy-admin", env("EC_CADDY_ADMIN", "http://127.0.0.1:2019"), "本机 Caddy Admin 地址")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg.Log = log
	cfg.Version = "0.1.0"

	if cfg.MasterAddr == "" || cfg.NodeID == "" {
		log.Error("--master 与 --node-id 必填")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a := agent.New(cfg)

	// 断开就重连，指数退避封顶 30 秒。隧道断了不代表节点该停止转发流量——
	// Caddy 仍在按最后一份配置服务，Agent 只是暂时失去控制面。
	backoff := time.Second
	for ctx.Err() == nil {
		err := a.Run(ctx)
		if ctx.Err() != nil {
			break
		}
		log.Warn("隧道断开，准备重连", "err", err, "after", backoff)
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	log.Info("agent 退出")
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
