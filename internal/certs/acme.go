package certs

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"

	"github.com/xltxb/edge_caddy/internal/dnsprovider"
	"github.com/xltxb/edge_caddy/internal/secret"
)

// AccountKeySetting 是 ACME 账号私钥在 settings 表里的键。
const AccountKeySetting = "acme.account_key"

// ACMEIssuer 用 DNS-01 向 ACME 服务器申请证书。
//
// 【本机验不了】这一层是 lego 的胶水：真跑一次需要真实域名 + DNS 服务商凭据 +
// 能出网访问 ACME 服务器。开发机上三样都没有，因此它被做成 Issuer 接口的一个
// 实现，**围绕它的调度逻辑**（续期窗口、退避、失败保留旧证书）在 manager_test.go
// 里是真测过的——那部分才是会把速率配额烧光的地方。
//
// 不用 lego 自带的 DNS provider：它们只做 TXT，而解析调度（工单 #15）需要完整的
// 记录管理。两套客户端意味着两份凭据处理，而凭据处理是最不该有第二份实现的地方。
type ACMEIssuer struct {
	dns       dnsprovider.TXTProvider
	directory string
	email     string
	store     Store
	master    []byte
	log       *slog.Logger
	// propagationTimeout 是等 DNS 记录全网可见的上限。
	propagationTimeout time.Duration
}

type ACMEConfig struct {
	DNS       dnsprovider.TXTProvider
	Directory string
	Email     string
	Store     Store
	Master    []byte
	Logger    *slog.Logger
	// PropagationTimeout 为 0 时用默认值。
	PropagationTimeout time.Duration
}

// DefaultPropagationTimeout 是等 TXT 记录全网可见的上限。
//
// 取 5 分钟：DNSPod 与 Cloudflare 通常几十秒内生效，但权威服务器之间同步偶尔
// 会慢。取太短会让签发在「记录已经写下去了但还没传开」时失败，而重试会消耗
// ACME 的失败校验配额（每小时 5 次）。
const DefaultPropagationTimeout = 5 * time.Minute

func NewACMEIssuer(c ACMEConfig) (*ACMEIssuer, error) {
	if c.DNS == nil {
		return nil, errors.New("尚未配置 DNS 服务商，无法用 DNS-01 签发")
	}
	if c.Email == "" {
		// ACME 账号必须绑邮箱：到期提醒、吊销通知都发到那里。
		// 没有它，出问题时唯一的外部提醒渠道就没了。
		return nil, errors.New("尚未填写 ACME 联系邮箱")
	}
	if c.Directory == "" {
		c.Directory = dnsprovider.LetsEncryptStaging
	}
	log := c.Logger
	if log == nil {
		log = slog.Default()
	}
	timeout := c.PropagationTimeout
	if timeout <= 0 {
		timeout = DefaultPropagationTimeout
	}
	return &ACMEIssuer{
		dns: c.DNS, directory: c.Directory, email: c.Email,
		store: c.Store, master: c.Master, log: log, propagationTimeout: timeout,
	}, nil
}

// acmeUser 是 lego 要求的账号载体。
type acmeUser struct {
	email string
	reg   *registration.Resource
	key   crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.reg }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// challengeProvider 把我们自己的 DNS 客户端接到 lego 的挑战流程上。
type challengeProvider struct {
	dns dnsprovider.TXTProvider
	log *slog.Logger
}

func (p *challengeProvider) Present(domain, _, keyAuth string) error {
	fqdn, value := dns01.GetRecord(domain, keyAuth)
	p.log.Info("写入 DNS-01 挑战记录", "domain", domain, "fqdn", fqdn)
	return p.dns.SetTXT(context.Background(), trimDot(fqdn), value)
}

func (p *challengeProvider) CleanUp(domain, _, keyAuth string) error {
	fqdn, value := dns01.GetRecord(domain, keyAuth)
	// 清理失败不该让签发失败：证书已经签出来了，残留一条 TXT 只是脏数据。
	// 但要留日志——攒多了会让下一次校验因为多条 TXT 而变慢。
	if err := p.dns.RemoveTXT(context.Background(), trimDot(fqdn), value); err != nil {
		p.log.Warn("清理 DNS-01 挑战记录失败", "domain", domain, "fqdn", fqdn, "err", err)
	}
	return nil
}

func trimDot(s string) string {
	if len(s) > 0 && s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}

// Issue 为一个域名签发证书。
func (a *ACMEIssuer) Issue(ctx context.Context, domain string) (Cert, error) {
	key, err := a.accountKey(ctx)
	if err != nil {
		return Cert{}, err
	}
	user := &acmeUser{email: a.email, key: key}

	cfg := lego.NewConfig(user)
	cfg.CADirURL = a.directory
	// 证书用 EC256：比 RSA 小、握手快，且所有现代浏览器都支持。
	// 老到不支持 ECDSA 的客户端在这个系统的场景里不存在。
	cfg.Certificate.KeyType = certcrypto.EC256

	client, err := lego.NewClient(cfg)
	if err != nil {
		return Cert{}, fmt.Errorf("建立 ACME 客户端: %w", err)
	}
	if err := client.Challenge.SetDNS01Provider(
		&challengeProvider{dns: a.dns, log: a.log},
		// 只用这几台公共解析器做传播检查：默认会去问域名的权威服务器，
		// 而权威服务器有时正是最后一个更新的那个
		dns01.AddRecursiveNameservers([]string{"1.1.1.1:53", "8.8.8.8:53"}),
		dns01.PropagationWait(a.propagationTimeout, true),
	); err != nil {
		return Cert{}, fmt.Errorf("装配 DNS-01 挑战: %w", err)
	}

	// 注册账号。已注册过时 lego 会返回既有的注册信息。
	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return Cert{}, fmt.Errorf("注册 ACME 账号: %w", err)
	}
	user.reg = reg

	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: []string{domain},
		Bundle:  true, // 带上中间证书：少了它，部分客户端会报「证书链不完整」
	})
	if err != nil {
		return Cert{}, fmt.Errorf("为 %s 签发证书: %w", domain, err)
	}

	c, err := Parse(domain, res.Certificate, res.PrivateKey)
	if err != nil {
		return Cert{}, err
	}
	c.Auto = true
	return c, nil
}

// accountKey 取出 ACME 账号私钥；没有就新建一把并**加密**落库。
//
// 账号私钥必须持久化：换一把等于换一个 ACME 账号，之前签过的证书与速率配额
// 全都不认了——而速率限制是按账号算的。
func (a *ACMEIssuer) accountKey(ctx context.Context) (crypto.PrivateKey, error) {
	if a.store == nil {
		return nil, errors.New("缺少存储，无法持久化 ACME 账号私钥")
	}
	type settingStore interface {
		GetSetting(ctx context.Context, key string) ([]byte, error)
		PutSetting(ctx context.Context, key string, val []byte) error
	}
	ss, ok := a.store.(settingStore)
	if !ok {
		return nil, errors.New("存储不支持读写设置")
	}

	blob, err := ss.GetSetting(ctx, AccountKeySetting)
	if err == nil && len(blob) > 0 {
		raw, err := secret.Open(blob, a.master)
		if err != nil {
			// 解不开就新建一把的话，等于悄悄换了 ACME 账号：之前签过的证书
			// 与速率配额全不认了，而速率限制是按账号算的
			return nil, fmt.Errorf("解密 ACME 账号私钥失败（主密钥是否变了？）: %w", err)
		}
		var p struct {
			KeyPEM []byte `json:"key"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("解析 ACME 账号私钥: %w", err)
		}
		block, _ := pem.Decode(p.KeyPEM)
		if block == nil {
			return nil, errors.New("ACME 账号私钥不是合法 PEM")
		}
		return x509.ParseECPrivateKey(block.Bytes)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成 ACME 账号私钥: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string][]byte{
		"key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}),
	})
	if err != nil {
		return nil, err
	}
	sealed, err := secret.Seal(raw, a.master)
	if err != nil {
		return nil, fmt.Errorf("加密 ACME 账号私钥: %w", err)
	}
	if err := ss.PutSetting(ctx, AccountKeySetting, sealed); err != nil {
		return nil, err
	}
	a.log.Info("已生成并保存 ACME 账号私钥")
	return key, nil
}
