package agent

import (
	"flag"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/xltxb/edge_caddy/internal/model"
)

// DefaultStateDir 是隧道证书与主控 CA 的落盘目录。
const DefaultStateDir = "/etc/edge-agent/pki"

// DefaultCaddyAdmin 是本机 Caddy Admin API 地址。
//
// 只连回环：Admin API 没有任何鉴权，能访问的人等于拥有这台节点——可以改回源、
// 读走全部配置。部署脚本会把它固定在回环上并用防火墙做第二道防线。
const DefaultCaddyAdmin = "http://127.0.0.1:2019"

// VerifyOff 是显式关闭校验端点的写法。
//
// 用一个说得出口的词，而不是「留空」：留空看不出是有意关掉还是忘了填，
// 而这两者的后果差得很远——受保护域名的 fail-closed 依赖它存活（ADR-0003）。
const VerifyOff = "off"

// ParseArgs 解析命令行，返回运行配置与子命令。
//
// 抽成函数是为了让它**可测**。这些默认值原先内联在 cmd/agent 的 main() 里，
// 结果是从没有测试走过它们——VerifyAddr 一直没被设置，访问规则在生产上
// 完全不工作，而单测与 e2e 全绿（它们都直接构造 Config）。
func ParseArgs(args []string) (Config, string, error) {
	if len(args) == 0 {
		return Config{}, "", fmt.Errorf("缺少子命令，用法: edge-agent <enroll|run> --node-id <ID> --master <host:port>")
	}
	cmd := args[0]
	switch cmd {
	case "enroll", "run":
	default:
		return Config{}, "", fmt.Errorf("未知子命令 %q，只支持 enroll 与 run", cmd)
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // 用法信息由调用方决定怎么打，解析器只返回错误
	var (
		nodeID     = fs.String("node-id", "", "节点 ID，例如 node-hk-01")
		master     = fs.String("master", "", "主控地址 host:port")
		serverName = fs.String("server-name", "", "校验主控证书用的域名，默认取 master 的主机名")
		stateDir   = fs.String("state-dir", DefaultStateDir, "证书落盘目录")
		caPath     = fs.String("master-ca", "", "主控 CA 根证书路径；不给则用系统信任库")
		caddyAdmin = fs.String("caddy-admin", DefaultCaddyAdmin, "本机 Caddy Admin API 地址")
		verifyAddr = fs.String("verify-addr", model.DefaultVerifyAddr,
			"访问规则校验端点的监听地址，只能是回环；填 off 显式关闭")
	)
	if err := fs.Parse(args[1:]); err != nil {
		return Config{}, "", fmt.Errorf("解析参数: %w", err)
	}
	if *nodeID == "" {
		return Config{}, "", fmt.Errorf("必须指定 --node-id")
	}
	if *master == "" {
		return Config{}, "", fmt.Errorf("必须指定 --master")
	}

	addr := *verifyAddr
	switch {
	case addr == VerifyOff:
		addr = ""
	default:
		if err := checkLoopback(addr); err != nil {
			return Config{}, "", err
		}
	}

	name := *serverName
	if name == "" {
		name = hostOf(*master)
	}
	return Config{
		NodeID: *nodeID, MasterAddr: *master, ServerName: name,
		StateDir: *stateDir, CaddyAdmin: *caddyAdmin, VerifyAddr: addr,
		MasterCAPath: *caPath,
	}, cmd, nil
}

// checkLoopback 拒绝把校验端点放到回环之外。
//
// 它没有任何鉴权：能连上这个端口的人就能替别人做「这个请求算不算通过鉴权」
// 的决策。对外监听等于把受保护域名的门直接拆掉。
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("校验端点地址 %q 不是合法的 host:port: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("校验端点地址 %q 没有指定主机，会监听在所有网卡上；它没有鉴权，只能放回环", addr)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("校验端点地址 %q 不在回环上；它没有鉴权，能连上就能替别人做鉴权决策", addr)
	}
	return nil
}

// hostOf 取出 host:port 里的 host。
func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}
