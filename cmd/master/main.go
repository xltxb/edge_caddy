// Command master 是主控：HTTP 控制台接口 + 节点 gRPC 隧道。
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/auth"
	"github.com/xltxb/edge_caddy/internal/enroll"
	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

func main() {
	var (
		httpAddr = flag.String("http", ":8080", "控制台接口监听地址")
		grpcAddr = flag.String("grpc", ":9000", "节点隧道监听地址")
		dbPath   = flag.String("db", "edge.sqlite", "SQLite 数据库路径")
		hostname = flag.String("hostname", "master.local", "主控对节点公布的域名（证书主体）")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx := context.Background()

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// 主密钥用于加密 CA 私钥。没有它就拒绝启动——CA 私钥能为任意节点签发凭据，
	// 明文躺在库里是不会有任何东西提醒你的（见 internal/pki）。
	secret := []byte(os.Getenv("EDGE_SECRET_KEY"))
	if len(secret) == 0 {
		log.Error("必须设置 EDGE_SECRET_KEY（用于加密 CA 私钥）",
			"生成方式", "head -c 32 /dev/urandom | base64")
		os.Exit(1)
	}

	tunnelCA, err := pki.LoadOrCreate(ctx, st, pki.KeyTunnelCA, secret, "Edge Tunnel CA")
	if err != nil {
		log.Error("准备隧道 CA 失败", "err", err)
		os.Exit(1)
	}
	if _, err := pki.LoadOrCreate(ctx, st, pki.KeyUpstreamCA, secret, "Edge Upstream CA"); err != nil {
		log.Error("准备回源 CA 失败", "err", err)
		os.Exit(1)
	}

	authMgr := auth.New(st)
	if pw := os.Getenv("EDGE_ADMIN_PASSWORD"); pw != "" && !authMgr.Enabled(ctx) {
		if err := authMgr.SetPassword(ctx, pw); err != nil {
			log.Error("初始化管理员口令失败", "err", err)
			os.Exit(1)
		}
		log.Info("已用 EDGE_ADMIN_PASSWORD 初始化管理员口令", "user", auth.AdminUser)
	}
	if !authMgr.Enabled(ctx) {
		log.Warn("控制台接口当前无鉴权：任何能访问 " + *httpAddr + " 的人都可以改配置、签发接入凭据。设置 EDGE_ADMIN_PASSWORD 后重启即可启用")
	}

	enroller := enroll.New(st)
	tun := tunnel.NewServer(tunnel.Deps{CA: tunnelCA, Enroll: enroller, Store: st, Logger: log})

	serverCert, err := tunnelCA.IssueServer(*hostname, 365*24*time.Hour)
	if err != nil {
		log.Error("签发主控服务端证书失败", "err", err)
		os.Exit(1)
	}
	pair, err := tls.X509KeyPair(serverCert.CertPEM, serverCert.KeyPEM)
	if err != nil {
		log.Error("加载主控服务端证书失败", "err", err)
		os.Exit(1)
	}
	// VerifyClientCertIfGiven：接入时节点还没有证书，必须放行；
	// Channel 自己要求必须有已验证的证书链（见 internal/tunnel）。
	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{pair},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    tunnelCA.Pool(),
		MinVersion:   tls.VersionTLS12,
	})
	g := grpc.NewServer(grpc.Creds(creds))
	edgev1.RegisterEdgeEnrollServer(g, tun)
	edgev1.RegisterEdgeTunnelServer(g, tun)

	lis, err := net.Listen("tcp", *grpcAddr)
	if err != nil {
		log.Error("gRPC 监听失败", "addr", *grpcAddr, "err", err)
		os.Exit(1)
	}
	go func() {
		log.Info("gRPC 监听", "addr", *grpcAddr, "hostname", *hostname)
		if err := g.Serve(lis); err != nil {
			log.Error("gRPC 服务退出", "err", err)
		}
	}()

	h := api.New(api.Deps{Store: st, Auth: authMgr, Enroll: enroller, Logger: log})
	log.Info("HTTP 监听", "addr", *httpAddr)
	if err := http.ListenAndServe(*httpAddr, h); err != nil {
		log.Error("HTTP 服务退出", "err", err)
		os.Exit(1)
	}
}
