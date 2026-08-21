package certs

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/dnspod"
	"github.com/go-acme/lego/v4/registration"
	"github.com/xltxb/edge_caddy/internal/store"
)

// ACMEIssuer 用 DNS-01 向 ACME CA 申请证书。
//
// # 为什么是 lego 而不是 ADR-0001 里写的 certmagic
//
// 两者都能跑 DNS-01。选 lego 是因为它**自带** Cloudflare 与 DNSPod 的
// provider 实现，而 certmagic 走 libdns 接口，需要再引两个适配包。
// 少一层适配就少一处需要跟上游对齐的地方。ADR-0001 的结论（主控集中签发、
// DNS-01、凭据只在主控）完全不变，变的只是用哪个库。
//
// # 可信度
//
// **这条路径没有对真实 ACME 服务器验证过。** 验它需要一个公网可解析的域名、
// 一个真实的 DNS 服务商账号，而且会在 CA 那边留下真实的签发记录与速率配额消耗。
// 单测覆盖的是它周围的部分（存储、续期调度、下发、节点加载与回执），
// 那些都被真 Caddy 与真 PostgreSQL 验过。
//
// 首次接入时建议先指向 Let's Encrypt 的 **staging** 环境跑通一次——
// staging 的速率限制宽得多，签废了也不心疼。
type ACMEIssuer struct {
	Email     string
	Directory string // 空则用 Let's Encrypt 生产环境
	Provider  store.DNSProviderSettings
}

func (a *ACMEIssuer) Name() string {
	if a.Directory == lego.LEDirectoryStaging {
		return "Let's Encrypt (staging)"
	}
	return "Let's Encrypt"
}

// acmeUser 是 lego 要求的账户模型。
//
// 账户私钥每次现生成：这意味着每次签发都会注册一个新账户。对 6 个域名的
// 用量来说无所谓，但它值得写出来——真要长期跑，账户私钥应当持久化，
// 否则 ACME 那边会积累一堆一次性账户。
type acmeUser struct {
	email string
	key   *ecdsa.PrivateKey
	reg   *registration.Resource
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.reg }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

func (a *ACMEIssuer) Issue(ctx context.Context, domain string) ([]byte, []byte, time.Time, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	user := &acmeUser{email: a.Email, key: key}

	cfg := lego.NewConfig(user)
	if a.Directory != "" {
		cfg.CADirURL = a.Directory
	}
	cfg.Certificate.KeyType = certcrypto.EC256

	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("建立 ACME 客户端: %w", err)
	}

	provider, err := a.dnsProvider()
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	if err := client.Challenge.SetDNS01Provider(provider); err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("装配 DNS-01: %w", err)
	}

	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("注册 ACME 账户: %w", err)
	}
	user.reg = reg

	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: []string{domain}, Bundle: true,
	})
	if err != nil {
		// 原样带上 CA 的措辞：速率限制、DNS 校验超时、账户问题，
		// 三者的处置完全不同，而只有原文分得出来。
		return nil, nil, time.Time{}, fmt.Errorf("签发 %s: %w", domain, err)
	}

	notAfter, err := notAfterOfDER(res.Certificate)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return res.Certificate, res.PrivateKey, notAfter, nil
}

func (a *ACMEIssuer) dnsProvider() (challenge.Provider, error) {
	switch a.Provider.Kind {
	case "cloudflare":
		cfg := cloudflare.NewDefaultConfig()
		if a.Provider.CredentialMode == "global_key" {
			cfg.AuthEmail, cfg.AuthKey = a.Provider.Email, a.Provider.Credential
		} else {
			cfg.AuthToken = a.Provider.Credential
		}
		return cloudflare.NewDNSProviderConfig(cfg)
	case "dnspod":
		cfg := dnspod.NewDefaultConfig()
		cfg.LoginToken = a.Provider.Credential
		return dnspod.NewDNSProviderConfig(cfg)
	default:
		return nil, fmt.Errorf("DNS-01 需要一个 DNS 服务商，当前未配置")
	}
}

func notAfterOfDER(certPEM []byte) (time.Time, error) {
	blk, _ := pem.Decode(certPEM)
	if blk == nil {
		return time.Time{}, fmt.Errorf("ACME 返回的不是合法 PEM")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return c.NotAfter, nil
}
