package certs

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"
)

// Imported 是一张从外部证书平台拿来的证书，校验通过之后的样子。
type Imported struct {
	CertPEM  []byte
	KeyPEM   []byte
	NotAfter time.Time
	Issuer   string   // 证书里的签发者 CN，给人看
	Domains  []string // 证书覆盖的全部域名（SAN）

	// Leaf 是解出来的叶子证书。**留着它是为了让「覆盖不覆盖」只有一份判据**：
	// 调用方要问「这张证书管不管得着 a.example.com」时用
	// Leaf.VerifyHostname，与上面校验域名用的是同一个函数、同一套
	// RFC 通配符规则。手写 strings 比对会在 *.example.com 上判错。
	Leaf *x509.Certificate
	// Warnings 是**不该拒绝、但人应当知道**的事。
	//
	// 拒绝一张能用的证书，代价是人在别处凑合（比如把证书直接 scp 到节点上，
	// 绕开整套下发）；而不说的话，那些问题会在某个客户端上表现成
	// 「部分用户打不开」——最难查的一类。
	Warnings []string
}

// ValidateImport 校验一张要导入的证书。
//
// **五条里有四条是硬性的，因为它们的失败都不会当场显形**：证书存进库、
// 下发到节点、界面显示「已导入」，而站点是坏的。
//
//  1. PEM 能解析          —— 不然 Caddy 会拒绝整份配置（ADR-0004；
//     守着这条的是 TestSchemaErrorIsRejectedConsistently）
//  2. 私钥与证书匹配      —— 不匹配则 TLS 握手失败，而两边单独看都是合法 PEM
//  3. 域名在 SAN 里       —— 不在则浏览器报名称不符，而证书本身完全有效
//  4. 还没过期            —— 导入一张过期的证书，界面照样显示「已导入」
//  5. 链是否完整          —— **只警告**，见 Warnings 的说明
func ValidateImport(domain string, certPEM, keyPEM []byte) (Imported, error) {
	var out Imported

	// 1 + 2：让标准库同时做「能解析」和「私钥配得上」这两件事。
	// 手写比对公钥要分别处理 RSA / ECDSA / Ed25519，而 tls.X509KeyPair
	// 已经把那些都做完了。
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return out, fmt.Errorf("证书与私钥不匹配或格式不对：%w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return out, fmt.Errorf("解析证书失败：%w", err)
	}

	// 3：域名必须在 SAN 里。**用标准库的匹配**，它按 RFC 处理通配符——
	// 手写 strings 比对会在 *.example.com 上判错，而那正是最常见的一类证书。
	if err := leaf.VerifyHostname(domain); err != nil {
		return out, fmt.Errorf("这张证书不覆盖 %s（它覆盖的是 %s）",
			domain, strings.Join(certDomains(leaf), ", "))
	}

	// 4：过期的证书导入进来没有意义，而它会一路走到节点上。
	if now := time.Now(); leaf.NotAfter.Before(now) {
		return out, fmt.Errorf("这张证书已经在 %s 过期了",
			leaf.NotAfter.Format("2006-01-02"))
	} else if leaf.NotBefore.After(now) {
		return out, fmt.Errorf("这张证书要到 %s 才生效",
			leaf.NotBefore.Format("2006-01-02"))
	}

	out = Imported{
		CertPEM: certPEM, KeyPEM: keyPEM,
		NotAfter: leaf.NotAfter,
		Issuer:   issuerName(leaf),
		Domains:  certDomains(leaf),
		Leaf:     leaf,
	}

	// 5：只有叶子、没有中间证书。**不拒绝**——有些平台就是分开给的，
	// 而且部分客户端自带常见中间证书，装上去能用。
	//
	// 但要说出来：缺链的表现是**部分客户端握手失败**（Android 老版本、
	// 一些 Java 客户端），而另一些完全正常——「有的人打得开有的人打不开」
	// 是最难查的一类。
	if len(pair.Certificate) == 1 {
		out.Warnings = append(out.Warnings,
			"只有叶子证书，没有中间证书。部分客户端（老 Android、某些 Java "+
				"运行时）会握手失败，而另一些正常——建议把中间证书接在叶子后面一起提交")
	}
	if d := time.Until(leaf.NotAfter); d < 14*24*time.Hour {
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("这张证书 %.0f 天后到期。导入的证书主控不会自动续期，"+
				"到期前要再导一次", d.Hours()/24))
	}
	return out, nil
}

func certDomains(c *x509.Certificate) []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	add(c.Subject.CommonName)
	for _, d := range c.DNSNames {
		add(d)
	}
	return out
}

func issuerName(c *x509.Certificate) string {
	if n := c.Issuer.CommonName; n != "" {
		return n
	}
	if len(c.Issuer.Organization) > 0 {
		return c.Issuer.Organization[0]
	}
	return "未知签发者"
}

// DomainsOf 读出一张证书覆盖哪些域名。
//
// **不落库。** 证书本身就是这件事的唯一真相——存一份副本意味着两处，
// 而两处迟早会分叉（导入时解析对了、后来换了张证书而副本没更新，
// 界面就会说这张 `*.a.com` 覆盖的是 `b.com`）。
//
// 代价是每次列表都要解析 N 张证书，而 N 是域名数量，很小。
func DomainsOf(certPEM []byte) []string {
	blk, _ := pem.Decode(certPEM)
	if blk == nil {
		return nil
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil
	}
	return certDomains(c)
}

// CoversAny 说这张证书管不管得着这批域名里的任何一个。
//
// **判据是 VerifyHostname，与导入时校验域名用的是同一个函数。**
// 手写 strings 比对会在 *.example.com 上判错，而通配符证书正是最常见的那一类。
//
// 它回答的是一个具体问题：**这张证书推到节点上之后，会有人用到它吗？**
//
// 而这个问题不能问「有没有一条路由的域名等于它」：证书不是按路由挑的。
// 主控把**全部**证书内联进每个节点（ADR-0010），Caddy 在握手时按 SNI 自己配对
// ——所以一张 *.example.com 的证书会服务 a.example.com，哪怕没有任何一条
// 路由叫 *.example.com。用「等于」做判据会拒掉一张真的用得上的证书。
func (i Imported) CoversAny(domains []string) bool {
	return len(coveredBy(i.Leaf, domains)) > 0
}

// CoveredBy 回这张证书（PEM）覆盖得到 candidates 里的哪几个。
//
// 给库里已经存着的证书用——那时候手上只有 PEM，没有 Imported。
// **和 CoversAny 走同一个实现**：分开写的话，「收得进来」和「删得掉」
// 会对不上账，而那种不一致的症状是「导入时说它有用，删除时说它没用」。
//
// PEM 解不开时回 nil。调用方要自己决定那意味着什么——**它不等于「谁也没覆盖」**。
func CoveredBy(certPEM []byte, candidates []string) []string {
	blk, _ := pem.Decode(certPEM)
	if blk == nil {
		return nil
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil
	}
	return coveredBy(leaf, candidates)
}

func coveredBy(leaf *x509.Certificate, candidates []string) []string {
	if leaf == nil {
		return nil
	}
	var out []string
	for _, d := range candidates {
		if leaf.VerifyHostname(d) == nil {
			out = append(out, d)
		}
	}
	return out
}
