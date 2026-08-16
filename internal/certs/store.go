// Package certs 管理主控集中签发的服务端证书。
//
// 由主控签而不是各节点自签（ADR-0001）：Caddy 的 DNS provider 全是插件，
// 官方版一个都不内置；更要紧的是每台节点都得放一份能改写整个 zone 的 DNS API
// 凭据，而边缘节点是最暴露的一批机器。主控签发让凭据只存在一处。
//
// HTTP-01 在这个系统里不成立：域名按权重只解析到部分节点，轮换外的节点无法
// 完成校验，而节点恰恰需要在**进入轮换之前**就持有证书。
package certs

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/xltxb/edge_caddy/internal/secret"
)

// ErrNotFound 表示该域名还没有证书。
var ErrNotFound = errors.New("该域名还没有证书")

// Cert 是一张服务端证书及其私钥。
type Cert struct {
	Domain   string
	CertPEM  []byte
	KeyPEM   []byte
	NotAfter time.Time
	Issuer   string
	KeyType  string
	Serial   string
	// Auto 为真表示由主控自动续期；手工导入的证书为假。
	Auto     bool
	IssuedAt time.Time
}

// DaysLeft 返回距到期还有多少天。已过期时为负数。
func (c Cert) DaysLeft(now time.Time) int {
	return int(c.NotAfter.Sub(now).Hours() / 24)
}

// Store 是 certs 需要的存储能力。
type Store interface {
	DB() *sql.DB
}

// Parse 从 PEM 解出一张证书，元信息**从证书本身取**。
//
// 不靠调用方传：签发路径和导入路径会各填各的，而「签发者」这类字段填错了
// 没人看得出来。坏的 PEM 在这里就拒绝，不要等到下发到节点上才失败——
// Caddy 加载一个空 PEM 会拒绝整份 tls 配置，而那时的报错是「下发失败」。
func Parse(domain string, certPEM, keyPEM []byte) (Cert, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return Cert{}, fmt.Errorf("%s 的证书不是合法 PEM", domain)
	}
	x, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Cert{}, fmt.Errorf("解析 %s 的证书: %w", domain, err)
	}
	if kb, _ := pem.Decode(keyPEM); kb == nil {
		return Cert{}, fmt.Errorf("%s 的私钥不是合法 PEM", domain)
	}
	return Cert{
		Domain: domain, CertPEM: certPEM, KeyPEM: keyPEM,
		NotAfter: x.NotAfter.UTC().Truncate(time.Second),
		IssuedAt: x.NotBefore.UTC().Truncate(time.Second),
		Issuer:   x.Issuer.CommonName,
		KeyType:  keyTypeOf(x),
		// 序列号是换证时唯一能确认「真的换了」的字段：到期时间可能因为
		// 签发者的取整而看起来没变
		Serial: x.SerialNumber.Text(16),
		Auto:   true,
	}, nil
}

func keyTypeOf(x *x509.Certificate) string {
	switch x.PublicKeyAlgorithm {
	case x509.ECDSA:
		return "ECDSA " + x.SignatureAlgorithm.String()
	case x509.RSA:
		return "RSA " + x.SignatureAlgorithm.String()
	default:
		return x.PublicKeyAlgorithm.String()
	}
}

// Save 写入一张证书。同域名替换，不追加——续期后只该有一张。
func Save(ctx context.Context, st Store, master []byte, c Cert) error {
	// 私钥加密：拿到它就能冒充这个域名。证书本身不加密，它是公开信息。
	sealed, err := secret.Seal(c.KeyPEM, master)
	if err != nil {
		return fmt.Errorf("加密 %s 的私钥: %w", c.Domain, err)
	}
	auto := 0
	if c.Auto {
		auto = 1
	}
	_, err = st.DB().ExecContext(ctx, `
		INSERT INTO certificates (domain, cert_pem, key_pem, not_after, issuer, key_type, serial, auto, issued_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(domain) DO UPDATE SET
			cert_pem=excluded.cert_pem, key_pem=excluded.key_pem, not_after=excluded.not_after,
			issuer=excluded.issuer, key_type=excluded.key_type, serial=excluded.serial,
			auto=excluded.auto, issued_at=excluded.issued_at`,
		c.Domain, c.CertPEM, sealed, c.NotAfter.UTC().Format(time.RFC3339),
		c.Issuer, c.KeyType, c.Serial, auto, c.IssuedAt.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("写入 %s 的证书: %w", c.Domain, err)
	}
	return nil
}

// Get 取一张证书。
func Get(ctx context.Context, st Store, master []byte, domain string) (Cert, error) {
	row := st.DB().QueryRowContext(ctx, `
		SELECT domain, cert_pem, key_pem, not_after, issuer, key_type, serial, auto, issued_at
		FROM certificates WHERE domain = ?`, domain)
	c, err := scanCert(row, master)
	if errors.Is(err, sql.ErrNoRows) {
		return Cert{}, fmt.Errorf("%w: %s", ErrNotFound, domain)
	}
	return c, err
}

// All 返回全部证书，**按到期时间升序**——最紧急的排最前面。
func All(ctx context.Context, st Store, master []byte) ([]Cert, error) {
	rows, err := st.DB().QueryContext(ctx, `
		SELECT domain, cert_pem, key_pem, not_after, issuer, key_type, serial, auto, issued_at
		FROM certificates ORDER BY not_after ASC`)
	if err != nil {
		return nil, fmt.Errorf("读取证书列表: %w", err)
	}
	defer rows.Close()

	var out []Cert
	for rows.Next() {
		c, err := scanCert(rows, master)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Delete 删除一张证书。
func Delete(ctx context.Context, st Store, domain string) error {
	_, err := st.DB().ExecContext(ctx, `DELETE FROM certificates WHERE domain = ?`, domain)
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCert(row scanner, master []byte) (Cert, error) {
	var (
		c        Cert
		sealed   []byte
		notAfter string
		issuedAt string
		auto     int
	)
	if err := row.Scan(&c.Domain, &c.CertPEM, &sealed, &notAfter,
		&c.Issuer, &c.KeyType, &c.Serial, &auto, &issuedAt); err != nil {
		return Cert{}, err
	}
	key, err := secret.Open(sealed, master)
	if err != nil {
		// 主密钥不对时必须报错，不能当成「还没签过」返回空——那会让续期逻辑
		// 以为没有证书于是重新签，而 Let's Encrypt 是每域名每周 5 张，
		// 几次重启就把配额烧光了
		return Cert{}, fmt.Errorf("解密 %s 的私钥失败（主密钥是否变了？）: %w", c.Domain, err)
	}
	c.KeyPEM = key
	c.Auto = auto == 1
	c.NotAfter, _ = time.Parse(time.RFC3339, notAfter)
	c.IssuedAt, _ = time.Parse(time.RFC3339, issuedAt)
	return c, nil
}
