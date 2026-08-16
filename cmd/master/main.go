// Command master 是主控：HTTP 控制台接口 + 节点 gRPC 隧道。
//
// 这里只做「读配置、装配、监听」三件事。装配本身在 internal/master，
// 因为它需要被测试走一遍——内联在这里的时候，装配漏接组件是没人发现的
// （见 internal/master 的包注释）。
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/xltxb/edge_caddy/internal/master"
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

	secret := []byte(os.Getenv("EDGE_SECRET_KEY"))
	if len(secret) == 0 {
		log.Error("必须设置 EDGE_SECRET_KEY（用于加密 CA 私钥与告警渠道凭据）",
			"生成方式", "head -c 32 /dev/urandom | base64")
		os.Exit(1)
	}

	m, err := master.Assemble(ctx, master.Options{
		DBPath:        *dbPath,
		Hostname:      *hostname,
		Secret:        secret,
		AdminPassword: os.Getenv("EDGE_ADMIN_PASSWORD"),
		Logger:        log,
	})
	if err != nil {
		log.Error("装配主控失败", "err", err)
		os.Exit(1)
	}
	defer m.Close()

	if !m.AuthEnabled(ctx) {
		log.Warn("控制台接口当前无鉴权：任何能访问 " + *httpAddr + " 的人都可以改配置、签发接入凭据。设置 EDGE_ADMIN_PASSWORD 后重启即可启用")
	}

	m.RunBackground(ctx)

	lis, err := net.Listen("tcp", *grpcAddr)
	if err != nil {
		log.Error("gRPC 监听失败", "addr", *grpcAddr, "err", err)
		os.Exit(1)
	}
	go func() {
		log.Info("gRPC 监听", "addr", *grpcAddr, "hostname", *hostname)
		if err := m.GRPC.Serve(lis); err != nil {
			log.Error("gRPC 服务退出", "err", err)
		}
	}()

	log.Info("HTTP 监听", "addr", *httpAddr)
	if err := http.ListenAndServe(*httpAddr, m.HTTP); err != nil {
		log.Error("HTTP 服务退出", "err", err)
		os.Exit(1)
	}
}
