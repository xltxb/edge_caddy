// Command agent 跑在边缘节点上：接入主控，保持长连接并上报心跳。
//
// 用法：
//
//	edge-agent enroll --node-id node-hk-01 --master master.example.com:9000
//	edge-agent run    --node-id node-hk-01 --master master.example.com:9000
//
// 接入 Token 走环境变量 EDGE_ENROLL_TOKEN，不走命令行参数——
// 命令行参数会出现在 ps 输出里，任何本机用户都能看到。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/xltxb/edge_caddy/internal/agent"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var (
		nodeID     = fs.String("node-id", "", "节点 ID，例如 node-hk-01")
		master     = fs.String("master", "", "主控地址 host:port")
		serverName = fs.String("server-name", "", "校验主控证书用的域名，默认取 master 的主机名")
		stateDir   = fs.String("state-dir", "/etc/edge-agent/pki", "证书落盘目录")
		caPath     = fs.String("master-ca", "", "主控 CA 根证书路径；不给则用系统信任库")
		caddyAdmin = fs.String("caddy-admin", "http://127.0.0.1:2019", "本机 Caddy Admin API 地址")
	)
	_ = fs.Parse(os.Args[2:])

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if *nodeID == "" || *master == "" {
		log.Error("必须指定 --node-id 与 --master")
		os.Exit(1)
	}
	name := *serverName
	if name == "" {
		name = hostOf(*master)
	}

	cfg := agent.Config{
		NodeID: *nodeID, MasterAddr: *master, ServerName: name,
		StateDir: *stateDir, CaddyAdmin: *caddyAdmin, Logger: log,
	}
	if *caPath != "" {
		blob, err := os.ReadFile(*caPath)
		if err != nil {
			log.Error("读取主控 CA 失败", "path", *caPath, "err", err)
			os.Exit(1)
		}
		cfg.MasterCA = blob
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "enroll":
		token := os.Getenv("EDGE_ENROLL_TOKEN")
		if token == "" {
			log.Error("接入需要 EDGE_ENROLL_TOKEN 环境变量（不要用命令行参数传，ps 里看得到）")
			os.Exit(1)
		}
		if err := agent.Enroll(ctx, cfg, token); err != nil {
			log.Error("接入失败", "err", err)
			os.Exit(1)
		}
	case "run":
		if err := agent.Run(ctx, cfg); err != nil && ctx.Err() == nil {
			log.Error("运行失败", "err", err)
			os.Exit(1)
		}
	default:
		usage()
	}
}

func hostOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: edge-agent <enroll|run> --node-id <ID> --master <host:port>")
	os.Exit(2)
}
