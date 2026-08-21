package config

import (
	"fmt"
	"net"
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
func ValidateAdvertise(v string) error {
	if v == "" {
		// 与「填了 IP」分开措辞。混成一句会让人去找一个他根本没写过的地方。
		return fmt.Errorf("EC_ADVERTISE 未设置：它是主控对节点公布的地址，" +
			"会被写进每一台节点的启动参数。必须是**域名**而不是 IP，" +
			"例如 EC_ADVERTISE=ec.example.com:9000（本地开发可用 localhost:9000）")
	}

	host := v
	if h, _, err := net.SplitHostPort(v); err == nil {
		host = h
	}
	// IPv6 不带端口时 SplitHostPort 会失败，host 仍是原串；带端口时它已经把
	// 方括号剥掉了。两条路都走到 ParseIP，所以这里只需要兜住带方括号那种写法。
	host = strings.Trim(host, "[]")

	if host == "" {
		return fmt.Errorf("EC_ADVERTISE=%q 只有端口没有主机名："+
			"它会被写进每一台节点的启动参数，节点拿它连不上任何东西。"+
			"填一个**域名**，例如 ec.example.com:9000", v)
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("EC_ADVERTISE=%q 是 IP（%s），这里要求**域名**：\n"+
			"  主控换地址的那一天，IP 意味着每台节点的 --master 都要挨台改，"+
			"改完之前它们全部连不上；域名只需要改一条 DNS 记录。\n"+
			"  例如 EC_ADVERTISE=ec.example.com:9000（本地开发可用 localhost:9000）",
			v, host)
	}
	return nil
}
