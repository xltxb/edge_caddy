// master 是主控：Gin HTTP 面 + gRPC 隧道 + 调度器。
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/config"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/tunnel"
	"github.com/xltxb/edge_caddy/internal/ws"
)

func main() {
	migrateOnly := flag.Bool("migrate", false, "只执行数据库迁移然后退出")
	createUser := flag.String("create-user", "", "创建或重置一个控制台账号，格式 用户名:口令")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.LoadMaster()
	if err != nil {
		log.Error("配置无效", "err", err)
		os.Exit(1)
	}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := st.Migrate(); err != nil {
		log.Error("迁移失败", "err", err)
		os.Exit(1)
	}
	if *migrateOnly {
		log.Info("迁移完成")
		return
	}

	if *createUser != "" {
		name, pw, ok := splitOnce(*createUser, ':')
		if !ok || name == "" || pw == "" {
			log.Error("--create-user 格式应为 用户名:口令")
			os.Exit(1)
		}
		if err := st.CreateUser(ctx, name, pw); err != nil {
			log.Error("创建账号失败", "err", err)
			os.Exit(1)
		}
		log.Info("账号已就绪", "username", name)
		return
	}

	hub := ws.NewHub(log)

	tun := tunnel.New(log)
	go func() {
		if err := tun.Serve(cfg.GRPCAddr); err != nil {
			log.Error("gRPC 隧道退出", "err", err)
			os.Exit(1)
		}
	}()
	defer tun.Stop()

	srv := api.New(api.Options{
		Store:        st,
		Hub:          hub,
		Log:          log,
		SessionTTL:   cfg.SessionTTL,
		OpsBotToken:  cfg.OpsBotToken,
		SecureCookie: cfg.MTLSEnabled,
	})

	log.Info("HTTP 监听", "addr", cfg.HTTPAddr, "mtls", cfg.MTLSEnabled)
	if err := srv.Run(cfg.HTTPAddr); err != nil {
		log.Error("HTTP 服务退出", "err", err)
		os.Exit(1)
	}
}

func splitOnce(s string, sep byte) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
