// Package pki 管理主控持有的两套内部 CA。
//
// 两套 CA 相互独立（docs/adr/0009）：
//
//	隧道 CA —— 签发 Agent 连接主控用的客户端证书，信任方只有主控
//	回源 CA —— 签发边缘节点回源时出示的客户端证书，信任方是各源站
//
// 根私钥只存在于主控，边缘节点上不存在任何 CA 私钥。节点被攻破时攻击者拿到的
// 是一张叶子证书，不是签发权。
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// Issued 是一次签发的产物。
type Issued struct {
	Subject string
	CertPEM []byte
	KeyPEM  []byte
	// NotAfter 让调用方能安排续期，不必再解析一次证书。
	NotAfter time.Time
}

// CA 是一套内部证书颁发机构。
type CA struct {
	name string
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

// NewCA 生成一套新的自签 CA。
//
// 用 ECDSA P-256 而不是 RSA：密钥小、握手快，而这套证书会在每个回源连接上被用到。
func NewCA(name string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成 CA 私钥: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-5 * time.Minute), // 容忍节点与主控之间的小幅时钟偏差
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true, // 只签叶子，不签中间 CA
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("自签 CA 证书: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("解析 CA 证书: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{name: name, cert: cert, key: key, pool: pool}, nil
}

// IssueClient 为 subject 签发一张客户端证书。
//
// subject 是节点 ID：主控要能从连接直接判断对面是谁，不必再查一次库。
func (c *CA) IssueClient(subject string, ttl time.Duration) (Issued, error) {
	if subject == "" {
		return Issued{}, fmt.Errorf("签发客户端证书时缺少主体")
	}
	if ttl <= 0 {
		return Issued{}, fmt.Errorf("签发 %s 的证书时有效期无效: %v", subject, ttl)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Issued{}, fmt.Errorf("生成 %s 的私钥: %w", subject, err)
	}
	serial, err := randomSerial()
	if err != nil {
		return Issued{}, err
	}
	notAfter := time.Now().Add(ttl)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: subject},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return Issued{}, fmt.Errorf("签发 %s 的证书: %w", subject, err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return Issued{}, fmt.Errorf("序列化 %s 的私钥: %w", subject, err)
	}
	return Issued{
		Subject:  subject,
		CertPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:   pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		NotAfter: notAfter,
	}, nil
}

// Verify 校验一张证书是否由本 CA 签发且仍在有效期内。
func (c *CA) Verify(certPEM []byte) error {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("不是合法的 PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("解析证书: %w", err)
	}
	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     c.pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return fmt.Errorf("证书未通过 %s 的校验: %w", c.name, err)
	}
	return nil
}

// Pool 返回本 CA 的信任池，供 TLS 配置使用。
func (c *CA) Pool() *x509.CertPool { return c.pool }

// RootPEM 返回根证书，需要下发给对端用于验证。私钥永不离开主控。
func (c *CA) RootPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw})
}

func randomSerial() (*big.Int, error) {
	// 128 位随机序列号：CA/B Forum 的要求，也避免自增序列号泄漏签发量
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("生成序列号: %w", err)
	}
	return n, nil
}
