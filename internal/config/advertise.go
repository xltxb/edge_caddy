package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateAdvertise 要求主控公布地址是域名，不是 IP（#24）。
//
// 代价出现在**主控换地址那一天**：用 IP 的话，每台节点 EnvironmentFile 里的
// `--master` 都要挨台改，改完之前全部断连；用域名的话改一条 DNS 记录，节点无感。
//
// 贵的是「挨台改」，不是重签证书——内部 PKI 重签一次 SignServer 就行，
// 而服务端证书 TTL 是十年（pki.go），根本不会自动轮换。所以这笔账要么不付，
// 要么在换地址那天一次性全付。
//
// **首次接入并不看 SAN**（Agent 走 InsecureSkipVerify + --ca-pin 指纹校验），
// 所以这条限制跟「证书能不能验过」无关，纯粹是运维可达性。
//
// localhost 放行：它是主机名不是 IP，而本地开发要能跑。
//
// # 两种写法
//
//	ec.example.com:9000       直连 gRPC。端口是自己的，中间不能有 CDN。
//	wss://cdn.example.com     隧道走 WebSocket，穿 443。
//
// 后者是为了穿过只转发 80/443 的中间设施。灰度上撞到的：主控域名挂在
// Cloudflare 后面，CDN 不代理 9000——节点装完一切正常，
// 然后**永远不出现在控制台里**。
//
// **这个值原样进安装命令**（`--master <它>`），所以改这一个变量就够了，
// 不需要第二个「模式」开关。一个开关和一个地址能互相矛盾，
// 一个地址不能自相矛盾。
func ValidateAdvertise(v string) error {
	if u, ok := parseTunnelURL(v); ok {
		return validateHost(u, v)
	}
	// **带 scheme 而不是 ws/wss 的，明确拒掉。**
	//
	// 不拒的话它会掉进下面那条 host:port 的路：SplitHostPort 把
	// `https://cdn.example.com` 切成 host="https"，而 "https" 既不空、
	// 也不是 IP —— **校验平静地通过，然后节点拿着它连不上任何东西**。
	//
	// 悄悄当成 wss 也不行：写错的人学不到那个区别，
	// 而下一次他会在别的地方（比如 nginx 配置）再写错一遍。
	if strings.Contains(v, "://") {
		scheme, _, _ := strings.Cut(v, "://")
		return fmt.Errorf("EC_ADVERTISE=%q 的协议是 %q —— 只认两种写法：\n"+
			"  ec.example.com:9000     直连 gRPC 端口\n"+
			"  wss://cdn.example.com   隧道走 WebSocket，穿 443（域名可以在 CDN 后面）",
			v, scheme)
	}
	if v == "" {
		// 与「填了 IP」分开措辞。混成一句会让人去找一个他根本没写过的地方。
		return fmt.Errorf("EC_ADVERTISE 未设置：它是主控对节点公布的地址，" +
			"会被写进每一台节点的启动参数。必须是域名而不是 IP，" +
			"例如 EC_ADVERTISE=ec.example.com:9000（本地开发可用 localhost:9000）")
	}

	host := v
	if h, _, err := net.SplitHostPort(v); err == nil {
		host = h
	}
	return validateHost(host, v)
}

// validateHost 是两种写法共用的那一半：主机名不能空、不能是 IP。
func validateHost(host, v string) error {
	// IPv6 不带端口时 SplitHostPort 会失败，host 仍是原串；带端口时它已经把
	// 方括号剥掉了。两条路都走到 ParseIP，所以这里只需要兜住带方括号那种写法。
	host = strings.Trim(host, "[]")

	if host == "" {
		return fmt.Errorf("EC_ADVERTISE=%q 只有端口没有主机名："+
			"它会被写进每一台节点的启动参数，节点拿它连不上任何东西。"+
			"填一个域名，例如 ec.example.com:9000", v)
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("EC_ADVERTISE=%q 是 IP（%s），这里要求域名：\n"+
			"  主控换地址的那一天，IP 意味着每台节点的 --master 都要挨台改，"+
			"改完之前它们全部连不上；域名只需要改一条 DNS 记录。\n"+
			"  例如 EC_ADVERTISE=ec.example.com:9000（本地开发可用 localhost:9000）",
			v, host)
	}
	return nil
}

// parseTunnelURL 认出 `wss://host[:port][/path]` 这种写法，返回它的主机名。
//
// 只认 wss 与 ws。**http/https 要明确拒掉**，不能默默当成 wss：
// 一个把 `wss://` 写成 `https://` 的人，如果这里悄悄纠正了他，
// 他就学不到那个区别，而下一次他会在别的地方（比如 nginx 配置）再写错一遍。
func parseTunnelURL(v string) (host string, ok bool) {
	if !strings.Contains(v, "://") {
		return "", false
	}
	u, err := url.Parse(v)
	if err != nil {
		return "", false
	}
	if u.Scheme != "wss" && u.Scheme != "ws" {
		return "", false
	}
	return u.Hostname(), true
}

// TunnelIsHTTP 说这个公布地址走的是 HTTP 面那条隧道。
func TunnelIsHTTP(advertise string) bool {
	_, ok := parseTunnelURL(advertise)
	return ok
}

// AdvertiseHost 取出主机名，用来进服务端证书的 SAN。
//
// **两种写法都得走这里。** 直接 net.SplitHostPort 对 `wss://cdn.example.com`
// 会得出一个荒谬的结果（"wss" 或整串），而它的后果是**证书 SAN 里没有真正
// 那个主机名**——里层 TLS 握手报「证书不适用于该主机名」，
// 而人会去查证书，那儿没有问题。
func AdvertiseHost(advertise string) string {
	if h, ok := parseTunnelURL(advertise); ok {
		return h
	}
	if h, _, err := net.SplitHostPort(advertise); err == nil {
		return h
	}
	return advertise
}
