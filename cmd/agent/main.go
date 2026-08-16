// Command agent 跑在边缘节点上：接入主控，保持长连接并上报心跳。
//
// 用法：
//
//	edge-agent enroll --node-id node-hk-01 --master master.example.com:9000
//	edge-agent run    --node-id node-hk-01 --master master.example.com:9000
//
// 接入 Token 走环境变量 EDGE_ENROLL_TOKEN，不走命令行参数——
// 命令行参数会出现在 ps 输出里，任何本机用户都能看到。
//
// 参数解析在 internal/agent.ParseArgs，因为它需要被测试走一遍：内联在这里的
// 时候，--verify-addr 这个默认值从没被任何测试覆盖过，结果是校验端点在生产上
// 一直没起来，访问规则完全不工作（见 internal/agent/args.go）。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/xltxb/edge_caddy/internal/agent"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, cmd, err := agent.ParseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "用法: edge-agent <enroll|run> --node-id <ID> --master <host:port>")
		os.Exit(2)
	}
	cfg.Logger = log

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
	}
}
