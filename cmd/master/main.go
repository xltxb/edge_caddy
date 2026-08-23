// master 是主控：Gin HTTP 面 + gRPC 隧道 + 下发调度器。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
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
	"github.com/xltxb/edge_caddy/internal/traffic"
	"github.com/xltxb/edge_caddy/internal/tunnel"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// Version 由打包脚本用 -ldflags 注入（scripts/build.sh）。
var Version = "dev"

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

	// 版本进启动日志：灰度环境上「现在跑的是哪一版」要能从日志里直接读到，
	// 而不是靠人记得自己推了什么。
	log.Info("主控启动", "version", Version)

	// **控制台静态文件的位置解析成绝对路径，并当场说它在不在。**
	//
	// 默认值 `web/dist` 是**相对路径**，它的行为取决于工作目录：
	// 在仓库根目录起就能用，systemd 起（工作目录是 /）就不能。
	//
	// 前端 agent 撞到过这个形状的另一半：他拿掉 EC_WEB_ROOT 想验「没部署前端」
	// 那条路径，而它照样绿——因为他正好在仓库根目录起的主控。
	// **一个检查在他的环境里恒绿，而恒绿的原因跟它要验的东西无关。**
	//
	// 所以这里不等到有人访问才发现，启动就说清楚：它在找哪个绝对路径、
	// 那儿有没有东西。一行日志换掉一类「我以为部署了」。
	if abs, err := filepath.Abs(cfg.WebRoot); err == nil {
		cfg.WebRoot = abs
	}

	// **而「不在」和「读不到」要分开说。** 这里原先无论 os.Stat 报什么都说
	// 「不在」，err 被丢掉了——灰度上真实发生过：文件都在（root `ls` 看得见），
	// 而主控跑在 User=edge 下连目录都进不去，日志和页面都咬定「不在」。
	// 那句话把人送去查解包，而问题在权限。
	// 一句错的诊断比没有诊断更贵：它给了人一个方向，而那个方向是反的。
	switch _, err := os.Stat(filepath.Join(cfg.WebRoot, "index.html")); {
	case errors.Is(err, fs.ErrPermission):
		log.Warn("控制台静态文件读不到（没权限，不是不在），主控只提供 API",
			"web_root", cfg.WebRoot, "err", err,
			// **指整条路径，不是只指 web_root。** 权限逐层检查，
			// 坏的那一层可能跟这个项目毫无关系（现场就是 /opt 本身）。
			"提示", "先看 namei -l "+filepath.Join(cfg.WebRoot, "index.html")+
				"；主控不是 root，用它的身份验：sudo -u <该用户> test -r <路径>")
	case err != nil:
		log.Warn("控制台静态文件不在，主控只提供 API",
			"web_root", cfg.WebRoot, "err", err,
			"提示", "把前端产物解到那里，或者把 EC_WEB_ROOT 指过去")
	default:
		log.Info("控制台静态文件", "web_root", cfg.WebRoot)
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

	// 流量采样：每分钟一行全局聚合，只为总览的「较昨日同时段」同比（#25）。
	// 它自己会跳过主控刚启动的那几分钟——那时 health 的内存还是空的，
	// 采到的数字偏低，而 24 小时后它会成为分母。
	go (&traffic.Sampler{Store: st, Health: monitor, Log: log}).Run(ctx)

	// **两种写法都得走 AdvertiseHost。**
	//
	// 这里原先直接 SplitHostPort，而 `wss://cdn.example.com` 走它会得出
	// 一个荒谬的主机名 —— 后果是服务端证书的 SAN 里没有真正那个域名，
	// 里层 TLS 握手报「证书不适用于该主机名」，而人会去查证书，
	// 那儿没有问题。
	advertiseHost := config.AdvertiseHost(cfg.Advertise)
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

	// **主控不签发证书**（ADR-0015）。它只存、只下发、只在快到期时说出来。
	certMgr := certs.New(&certs.Manager{
		Store: st, Sealer: sealer, Hub: hub, Log: log, Alert: notifier,
		// 证书随每次下发内联带上（ADR-0010），所以导入之后必须触发一次下发——
		// 否则新证书会躺在库里，直到下一次有人改配置才下去。
		Redeploy: func(ctx context.Context, reason string) error {
			_, _, err := scheduler.Deploy(ctx, "system", nil)
			return err
		},
	})
	// 每天扫一次到期。**拆掉自动续期之后，这是「证书要过期了」
	// 唯一会主动找人的地方** —— 漏了这一行，证书会安静地走到到期那一天。
	go certMgr.Run(ctx, 24*time.Hour)

	srv := api.New(api.Options{
		Store: st, Hub: hub, Tunnel: tun, Health: monitor, Alerts: notifier, DNS: dnsOrch,
		Sealer: sealer, Deployer: scheduler, Certs: certMgr, Log: log,
		SessionTTL: cfg.SessionTTL, OpsBotToken: cfg.OpsBotToken, WebRoot: cfg.WebRoot,
		Version:        Version,
		SecureCookie:   cfg.SecureCookie,
		TrustedProxies: cfg.TrustedProxies,
		MasterAddr:     cfg.Advertise, CAPin: caPin,
	})

	log.Info("HTTP 监听", "addr", cfg.HTTPAddr, "mtls", cfg.MTLSEnabled, "ca_pin", caPin)
	if err := srv.Run(cfg.HTTPAddr); err != nil {
		log.Error("HTTP 服务退出", "err", err)
		os.Exit(1)
	}
}
