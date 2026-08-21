// master 是主控：Gin HTTP 面 + gRPC 隧道 + 下发调度器。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/xltxb/edge_caddy/internal/alert"
	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/certs"
	"github.com/xltxb/edge_caddy/internal/config"
	"github.com/xltxb/edge_caddy/internal/deploy"
	"github.com/xltxb/edge_caddy/internal/dnsops"
	"github.com/xltxb/edge_caddy/internal/health"
	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/render"
	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/tunnel"
	"github.com/xltxb/edge_caddy/internal/ws"
)

func main() {
	migrateOnly := flag.Bool("migrate", false, "只执行数据库迁移然后退出")
	createUser := flag.String("create-user", "", "创建或重置一个控制台账号，格式 用户名:口令")
	showPin := flag.Bool("ca-pin", false, "打印隧道 CA 指纹然后退出")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.LoadMaster()
	if err != nil {
		// **配置错误走 stderr，不走结构化日志。**
		//
		// JSON handler 会把换行压成 \n 的转义串，而这些错误信息是**写给人现场
		// 照着改的**——它们有多行、有示例命令，被转义之后基本读不了。
		//
		// 更根本的是：这一刻还没有「运行中的服务」，没有别的日志跟它汇聚，
		// 也没有采集器在读。结构化的唯一好处（能被机器捞出来）在这里不存在，
		// 而代价（人读不了）全额付出。
		fmt.Fprintf(os.Stderr, "配置无效：%v\n", err)
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
		name, pw, ok := strings.Cut(*createUser, ":")
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

	sealer, err := secret.New(cfg.SecretKey)
	if err != nil {
		log.Error("初始化密封器失败", "err", err)
		os.Exit(1)
	}

	// 隧道 CA 不存在就自动生成。一个必须手工初始化才能工作的控制面，
	// 会在「重装了一台主控」那天以「节点全都连不上」的形式失败。
	ca, err := st.EnsureCA(ctx, pki.KindTunnel, sealer)
	if err != nil {
		log.Error("准备隧道 CA 失败", "err", err)
		os.Exit(1)
	}
	caPin, err := pki.Fingerprint(ca.CertPEM)
	if err != nil {
		log.Error("计算 CA 指纹失败", "err", err)
		os.Exit(1)
	}
	if *showPin {
		os.Stdout.WriteString(caPin + "\n")
		return
	}

	// 主控重启时内存里的重试队列没了，库里 retrying=true 的行会让 phase
	// 永远停在 running —— 弹层永远不落定，而它等的那次重试再也不会发生。
	if n, err := st.ClearStaleRetries(ctx); err != nil {
		log.Error("清理中断的重试失败", "err", err)
	} else if n > 0 {
		log.Warn("清理了上次运行遗留的重试", "rows", n)
	}

	hub := ws.NewHub(log)
	notifier := alert.New(st, sealer, log)
	dnsOrch := &dnsops.Orchestrator{Store: st, Sealer: sealer, Log: log}

	sys, err := st.GetSystemSettings(ctx)
	if err != nil {
		log.Error("读取系统设置失败", "err", err)
		os.Exit(1)
	}
	monitor := health.New(health.Config{
		Store: st, Hub: hub, Log: log, Alert: notifier, DNS: dnsOrch,
		Interval:   time.Duration(sys.HeartbeatInterval) * time.Second,
		Threshold:  sys.OfflineThreshold,
		WarnCPUPct: sys.WarnCPUPct,
		WarnMemPct: sys.WarnMemPct,
	})
	go monitor.Run(ctx)

	advertiseHost, _, err := net.SplitHostPort(cfg.Advertise)
	if err != nil {
		advertiseHost = cfg.Advertise
	}
	tun, err := tunnel.New(tunnel.Options{
		Store: st, CA: ca, Log: log,
		Advertise: []string{advertiseHost, "127.0.0.1", "localhost"},
		OnHeartbeat: func(hb tunnel.Heartbeat) string {
			status := monitor.Observe(hb)
			hub.Broadcast(ws.TypeHeartbeat, ws.Heartbeat{
				ID: hb.NodeID, Status: status, CPU: hb.CPU, Mem: hb.Mem,
				Conns: hb.Conns, HBAgeMS: 0, CfgVersion: hb.CfgVersion,
				Routes: hb.Routes, Rules: hb.Rules,
			})
			return status
		},
	})
	if err != nil {
		log.Error("装配隧道失败", "err", err)
		os.Exit(1)
	}
	go func() {
		if err := tun.ListenAndServe(cfg.GRPCAddr); err != nil {
			log.Error("gRPC 隧道退出", "err", err)
			os.Exit(1)
		}
	}()
	defer tun.Stop()

	// 回源 CA 与隧道 CA 相互独立、根私钥都只在主控（ADR-0009）。
	upstreamCA, err := st.EnsureCA(ctx, pki.KindUpstream, sealer)
	if err != nil {
		log.Error("准备回源 CA 失败", "err", err)
		os.Exit(1)
	}

	scheduler := &deploy.Scheduler{
		Store: st, Pusher: tun, Hub: hub, Log: log, Sealer: sealer, UpstreamCA: upstreamCA,
		Render: render.Options{
			HTTPListen:         cfg.EdgeHTTPListen,
			HTTPSListen:        cfg.EdgeHTTPSListen,
			VerifyAddr:         cfg.VerifyAddr,
			UpstreamClientCert: cfg.UpstreamCert,
			UpstreamClientKey:  cfg.UpstreamKey,
		},
	}

	dnsProvider, err := st.GetDNSProvider(ctx, sealer)
	if err != nil {
		log.Error("读取 DNS 服务商设置失败", "err", err)
		os.Exit(1)
	}
	certMgr := certs.New(&certs.Manager{
		Store: st, Sealer: sealer, Hub: hub, Log: log,
		Issuer: &certs.ACMEIssuer{
			Email:     cfg.ACMEEmail,
			Directory: cfg.ACMEDirectory,
			Provider:  dnsProvider,
		},
		// 证书随每次下发内联带上（ADR-0010），所以续期之后必须触发一次下发——
		// 否则新证书会躺在库里，直到下一次有人改配置才下去。
		Redeploy: func(ctx context.Context, reason string) error {
			_, _, err := scheduler.Deploy(ctx, "system", nil)
			return err
		},
	})
	go certMgr.Run(ctx, 12*time.Hour)
	scheduler.EnsureCerts = certMgr.EnsureFor

	srv := api.New(api.Options{
		Store: st, Hub: hub, Tunnel: tun, Health: monitor, Alerts: notifier, DNS: dnsOrch,
		Sealer: sealer, Deployer: scheduler, Certs: certMgr, Log: log,
		SessionTTL: cfg.SessionTTL, OpsBotToken: cfg.OpsBotToken,
		SecureCookie: cfg.MTLSEnabled,
		MasterAddr:   cfg.Advertise, CAPin: caPin,
	})

	log.Info("HTTP 监听", "addr", cfg.HTTPAddr, "mtls", cfg.MTLSEnabled, "ca_pin", caPin)
	if err := srv.Run(cfg.HTTPAddr); err != nil {
		log.Error("HTTP 服务退出", "err", err)
		os.Exit(1)
	}
}
