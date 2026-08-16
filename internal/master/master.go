// Package master 把主控的各个组件装配成一个可运行的整体。
//
// 装配单独成包是为了让它**可测**。它原先内联在 cmd/master/main() 里，结果是
// 装配这一步从来没有被任何测试走过：工单 #8 加了三个节点操作接口，单测与 e2e
// 全绿，而 main 里的 api.Deps 从没填过 Nodes——真跑起来那三个接口一律返回
// 「节点通道未就绪」。
package master

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/alert"
	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/auth"
	"github.com/xltxb/edge_caddy/internal/deploy"
	"github.com/xltxb/edge_caddy/internal/enroll"
	"github.com/xltxb/edge_caddy/internal/health"
	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/tunnel"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// ServerCertTTL 是主控对节点公布的服务端证书有效期。
const ServerCertTTL = 365 * 24 * time.Hour

type Options struct {
	DBPath   string
	Hostname string
	// Secret 是主密钥，用于加密 CA 私钥与告警渠道凭据。为空则拒绝装配。
	Secret []byte
	// AdminPassword 非空且尚未设过口令时用它初始化。
	AdminPassword string
	Logger        *slog.Logger
}

// Master 是装配好的主控。
type Master struct {
	HTTP    http.Handler
	GRPC    *grpc.Server
	Store   *store.Store
	Tunnel  *tunnel.Server
	Checker *health.Checker
	Alerts  *alert.Notifier
	Hub     *ws.Hub
	Auth    *auth.Manager

	log *slog.Logger
}

// Assemble 装配主控。它不监听任何端口——那是调用方的事。
func Assemble(ctx context.Context, o Options) (*Master, error) {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stdout, nil))
	}
	// 主密钥能解开 CA 私钥，而 CA 私钥能为任意节点签发凭据。没有它就拒绝启动，
	// 不做「先明文存着以后再加密」——那个「以后」不会到来。
	if len(o.Secret) == 0 {
		return nil, errors.New("必须提供主密钥（EDGE_SECRET_KEY），用于加密 CA 私钥与渠道凭据")
	}
	if o.Hostname == "" {
		return nil, errors.New("必须提供主控域名：证书是签给域名的，用 IP 连接则无从校验主体")
	}

	st, err := store.Open(o.DBPath)
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}

	tunnelCA, err := pki.LoadOrCreate(ctx, st, pki.KeyTunnelCA, o.Secret, "Edge Tunnel CA")
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("准备隧道 CA: %w", err)
	}
	upstreamCA, err := pki.LoadOrCreate(ctx, st, pki.KeyUpstreamCA, o.Secret, "Edge Upstream CA")
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("准备回源 CA: %w", err)
	}

	authMgr := auth.New(st)
	if o.AdminPassword != "" && !authMgr.Enabled(ctx) {
		if err := authMgr.SetPassword(ctx, o.AdminPassword); err != nil {
			_ = st.Close()
			return nil, fmt.Errorf("初始化管理员口令: %w", err)
		}
		log.Info("已初始化管理员口令", "user", auth.AdminUser)
	}

	enroller := enroll.New(st)
	hub := ws.NewHub()
	tun := tunnel.NewServer(tunnel.Deps{CA: tunnelCA, Enroll: enroller, Store: st, Hub: hub, Logger: log})
	orch := deploy.New(st, tun, log)
	// 隧道要把节点回报的结果转交给编排器；编排器又要用隧道发送。
	// 用 setter 打破这个环，比引入一个中间事件总线简单。
	tun.SetResults(orch)
	orch.SetBroadcaster(hub)
	orch.SetUpstreamIssuer(pki.NewUpstreamIssuer(upstreamCA, nil))

	// 告警通知器包住 hub：事件帧原样转给控制台，同时按级别过滤后送外部渠道。
	// 挂在这一层而不是让每个发事件的地方各调一次告警——后者迟早会漏掉一处。
	notifier := alert.New(alert.Deps{Inner: hub, Logger: log})
	alertCfg, err := alert.Load(ctx, st, o.Secret)
	if err != nil {
		notifier.Close()
		_ = st.Close()
		// 解不开说明主密钥变了。静默用默认设置会让告警悄悄停摆，
		// 而面板上显示「未配置」——出事那天才发现没人收到通知。
		return nil, fmt.Errorf("读取告警设置: %w", err)
	}
	notifier.SetConfig(alertCfg)

	// 健康巡检：心跳连续超时即判离线并发事件。判离线不做补救动作
	// （摘 DNS 属工单 #15），只更新状态让人看得见。
	checker := health.New(st, notifier, health.Config{Logger: log})

	serverCert, err := tunnelCA.IssueServer(o.Hostname, ServerCertTTL)
	if err != nil {
		notifier.Close()
		_ = st.Close()
		return nil, fmt.Errorf("签发主控服务端证书: %w", err)
	}
	pair, err := tls.X509KeyPair(serverCert.CertPEM, serverCert.KeyPEM)
	if err != nil {
		notifier.Close()
		_ = st.Close()
		return nil, fmt.Errorf("加载主控服务端证书: %w", err)
	}
	// VerifyClientCertIfGiven：接入时节点还没有证书，必须放行；
	// Channel 自己要求必须有已验证的证书链（见 internal/tunnel）。
	g := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{pair},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    tunnelCA.Pool(),
		MinVersion:   tls.VersionTLS12,
	})))
	edgev1.RegisterEdgeEnrollServer(g, tun)
	edgev1.RegisterEdgeTunnelServer(g, tun)

	h := api.New(api.Deps{
		Store: st, Auth: authMgr, Enroll: enroller, Deploy: orch, Nodes: tun,
		Hub: hub, Alerts: notifier, Secret: o.Secret, Logger: log,
	})

	return &Master{
		HTTP: h, GRPC: g, Store: st, Tunnel: tun,
		Checker: checker, Alerts: notifier, Hub: hub, Auth: authMgr, log: log,
	}, nil
}

// AuthEnabled 报告控制台接口是否需要登录。
func (m *Master) AuthEnabled(ctx context.Context) bool {
	return m.Auth.Enabled(ctx)
}

// Close 释放装配出来的资源。
func (m *Master) Close() {
	m.GRPC.Stop()
	m.Alerts.Close()
	if err := m.Store.Close(); err != nil {
		m.log.Error("关闭数据库失败", "err", err)
	}
}
