package dnsprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
)

// SettingKey 是 DNS 服务商设置在 settings 表里的键。
const SettingKey = "dns.provider"

// Kind 是服务商种类。
type Kind string

const (
	KindCloudflare Kind = "cloudflare"
	KindDNSPod     Kind = "dnspod"
)

// ACME 目录地址。
//
// 默认走 **staging**：正式环境的速率限制是每个注册域名每周 5 张证书，
// 而调试阶段一定会反复试。默认指向正式环境等于让第一次配错就烧掉一周的配额。
const (
	LetsEncryptStaging    = "https://acme-staging-v02.api.letsencrypt.org/directory"
	LetsEncryptProduction = "https://acme-v02.api.letsencrypt.org/directory"
)

// Config 是 DNS 服务商与 ACME 的设置。凭据字段**只写入不回显**。
type Config struct {
	Kind Kind `json:"kind"`

	DNSPodID    string `json:"dnspod_id"`
	DNSPodToken string `json:"dnspod_token"`

	CloudflareToken string `json:"cloudflare_token"`

	// ACMEEmail 不是凭据：它是运维确认「配的是哪个账号」的唯一线索，可以回显。
	ACMEEmail     string `json:"acme_email"`
	ACMEDirectory string `json:"acme_directory"`

	// 清除标记只在提交时有意义，不落库。
	ClearDNSPod     bool `json:"-"`
	ClearCloudflare bool `json:"-"`
}

// PublicConfig 是对外表示：没有任何凭据，只说「配没配」。
type PublicConfig struct {
	Kind                 Kind   `json:"kind"`
	DNSPodConfigured     bool   `json:"dnspod_configured"`
	CloudflareConfigured bool   `json:"cloudflare_configured"`
	ACMEEmail            string `json:"acme_email"`
	ACMEDirectory        string `json:"acme_directory"`
	// Staging 让界面能显眼地提示「现在签出来的证书浏览器不认」。
	Staging bool `json:"staging"`
}

func (c Config) Public() PublicConfig {
	return PublicConfig{
		Kind:                 c.Kind,
		DNSPodConfigured:     c.DNSPodID != "" && c.DNSPodToken != "",
		CloudflareConfigured: c.CloudflareToken != "",
		ACMEEmail:            c.ACMEEmail,
		ACMEDirectory:        c.ACMEDirectory,
		Staging:              c.ACMEDirectory != LetsEncryptProduction,
	}
}

// Store 是配置存取需要的能力。
type Store interface {
	GetSetting(ctx context.Context, key string) ([]byte, error)
	PutSetting(ctx context.Context, key string, val []byte) error
}

func Save(ctx context.Context, st Store, master []byte, c Config) error {
	if c.ACMEDirectory == "" {
		c.ACMEDirectory = LetsEncryptStaging
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("序列化 DNS 服务商设置: %w", err)
	}
	// 整份加密：只加密凭据字段的话，日后加一个新的凭据字段很容易忘了纳进来，
	// 而忘了是不会有任何提示的。
	sealed, err := secret.Seal(raw, master)
	if err != nil {
		return fmt.Errorf("加密 DNS 服务商设置: %w", err)
	}
	return st.PutSetting(ctx, SettingKey, sealed)
}

func Load(ctx context.Context, st Store, master []byte) (Config, error) {
	def := Config{ACMEDirectory: LetsEncryptStaging}
	blob, err := st.GetSetting(ctx, SettingKey)
	if errors.Is(err, store.ErrNotFound) || (err == nil && len(blob) == 0) {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	raw, err := secret.Open(blob, master)
	if err != nil {
		return def, fmt.Errorf("解密 DNS 服务商设置失败（主密钥是否变了？）: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return def, fmt.Errorf("解析 DNS 服务商设置: %w", err)
	}
	if c.ACMEDirectory == "" {
		c.ACMEDirectory = LetsEncryptStaging
	}
	return c, nil
}

// Merge 用一份提交覆盖已存设置：凭据字段留空表示**不改动**。
//
// 前端拿不到明文，提交时那几个框必然是空的。把空当成清空的话，切一次服务商
// 就顺手把凭据抹了，而界面上一切正常——直到下次续期失败。
func Merge(ctx context.Context, st Store, master []byte, in Config) error {
	cur, err := Load(ctx, st, master)
	if err != nil {
		return err
	}
	next := cur
	if in.Kind != "" {
		next.Kind = in.Kind
	}
	if in.ACMEEmail != "" {
		next.ACMEEmail = in.ACMEEmail
	}
	if in.ACMEDirectory != "" {
		next.ACMEDirectory = in.ACMEDirectory
	}
	next.DNSPodID = mergeSecret(cur.DNSPodID, in.DNSPodID, in.ClearDNSPod)
	next.DNSPodToken = mergeSecret(cur.DNSPodToken, in.DNSPodToken, in.ClearDNSPod)
	next.CloudflareToken = mergeSecret(cur.CloudflareToken, in.CloudflareToken, in.ClearCloudflare)
	return Save(ctx, st, master, next)
}

func mergeSecret(cur, in string, clear bool) string {
	switch {
	case clear:
		return ""
	case in == "":
		return cur
	default:
		return in
	}
}

// New 按设置建出客户端。
//
// 凭据没配全时**拒绝构造**，而不是造一个注定 401 的客户端：造出来的话，
// 第一次真调用才失败，而那时的报错是服务商给的「登录失败」——跟
// 「你还没填凭据」差得很远，人会去查凭据对不对，而其实根本没填。
func New(c Config) (Provider, error) {
	switch c.Kind {
	case KindCloudflare:
		if c.CloudflareToken == "" {
			return nil, errors.New("尚未填写 Cloudflare API Token")
		}
		return NewCloudflare(CloudflareConfig{APIToken: c.CloudflareToken}), nil
	case KindDNSPod:
		if c.DNSPodID == "" {
			return nil, errors.New("尚未填写 DNSPod ID")
		}
		if c.DNSPodToken == "" {
			return nil, errors.New("尚未填写 DNSPod Token")
		}
		return NewDNSPod(DNSPodConfig{ID: c.DNSPodID, Token: c.DNSPodToken}), nil
	case "":
		return nil, errors.New("尚未选择 DNS 服务商")
	default:
		return nil, fmt.Errorf("不支持的 DNS 服务商 %q（目前支持 cloudflare / dnspod）", c.Kind)
	}
}
