// agent 跑在边缘节点上：一条到主控的 gRPC 长连接，加上对本机 Caddy 的托管。
//
// 它不做决策，只执行与回报（CONTEXT.md「Agent」）。
package main

import (
	"log/slog"
	"os"

	"github.com/xltxb/edge_caddy/internal/config"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.LoadAgent()
	if err != nil {
		log.Error("配置无效", "err", err)
		os.Exit(1)
	}

	log.Info("agent 启动",
		"node_id", cfg.NodeID, "master", cfg.MasterAddr, "caddy_admin", cfg.CaddyAdmin)

	// 隧道与 Caddy 托管在 issue #18 落地。
	log.Error("隧道尚未实现（issue #18）")
	os.Exit(1)
}
