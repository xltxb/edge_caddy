// Package pki 是主控持有的内部证书机构。
//
// 系统里有两处 mTLS，用途完全不同，各用一套**相互独立**的 CA
// （见 docs/adr/0009-internal-pki-two-cas.md）：
//
//   - 隧道 CA —— Agent 向主控证明身份。信任方只有主控，叶子长期。
//   - 回源 CA —— 边缘节点向源站证明身份。信任方是各源站，叶子 24 小时。
//
// 不共用一个根：换 edge-mtls 根要协调每一个源站的信任库，共用意味着源站侧的
// 一次轮换会同时把所有节点踢下控制面——而那正是你最需要控制面去下发新证书的时刻。
//
// 两套 CA 的根私钥都只存在于主控，边缘节点上不存在任何 CA 私钥。
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// Kind 区分两套 CA。字符串与 pki_cas.kind 的 CHECK 约束一致。
type Kind string

const (
	KindTunnel   Kind = "tunnel"
	KindUpstream Kind = "upstream"
)

// tunnelLeafTTL 取长期。短了会死锁：隧道证书过期 → 连不上主控 → 拿不到新证书
// → 永远连不上。万一真的过期（节点停机数月），恢复路径是重新走接入流程。
const tunnelLeafTTL = 10 * 365 * 24 * time.Hour

const caTTL = 20 * 365 * 24 * time.Hour

// CA 是一套已经装配好的证书机构。
type CA struct {
	Kind    Kind
	CertPEM []byte
	KeyPEM  []byte

	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// GenerateCA 新建一套 CA。
func GenerateCA(kind Kind) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成 CA 私钥: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: fmt.Sprintf("Edge Controller %s CA", kind)},
		NotBefore:             now.Add(-time.Hour), // 容忍主控与节点之间的少量时钟偏差
		NotAfter:              now.Add(caTTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("自签 CA 证书: %w", err)
	}
	return assemble(kind, der, key)
}

// LoadCA 从已有的 PEM 装配。
func LoadCA(kind Kind, certPEM, keyPEM []byte) (*CA, error) {
	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(keyPEM)
	if blk == nil {
		return nil, fmt.Errorf("CA 私钥不是合法 PEM")
	}
	key, err := x509.ParseECPrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析 CA 私钥: %w", err)
	}
	return &CA{Kind: kind, CertPEM: certPEM, KeyPEM: keyPEM, cert: cert, key: key}, nil
}

// Leaf 是一张签出来的叶子证书。
type Leaf struct {
	CertPEM []byte
	KeyPEM  []byte
}

// SignClient 为一个节点签发客户端证书。CN 是 node_id——主控据此认出对端是谁，
// 所以隧道上的身份不来自 Agent 的自称，而来自它出示的证书。
func (c *CA) SignClient(nodeID string, ttl time.Duration) (*Leaf, error) {
	return c.sign(pkix.Name{CommonName: nodeID}, ttl,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)
}

// SignServer 为主控自己签发服务端证书。hosts 里的 IP 与域名都进 SAN。
func (c *CA) SignServer(commonName string, hosts []string, ttl time.Duration) (*Leaf, error) {
	return c.sign(pkix.Name{CommonName: commonName}, ttl,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, hosts)
}

func (c *CA) sign(subj pkix.Name, ttl time.Duration, usage []x509.ExtKeyUsage, hosts []string) (*Leaf, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成叶子私钥: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      subj,
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  usage,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("签发叶子: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &Leaf{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// TunnelLeafTTL 是隧道叶子的有效期，导出供调用方使用。
func TunnelLeafTTL() time.Duration { return tunnelLeafTTL }

// Fingerprint 是证书 DER 的 SHA-256，十六进制小写。
//
// 它用在接入命令里：Agent 首连时手上还没有 CA，纯 TOFU 会让中间人在那一刻
// 冒充主控把一次性 Token 骗走。安装命令带上这个指纹，Agent 校验证书链的根
// 是否匹配，就把这个洞堵上了。
func Fingerprint(certPEM []byte) (string, error) {
	cert, err := parseCert(certPEM)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:]), nil
}

func assemble(kind Kind, der []byte, key *ecdsa.PrivateKey) (*CA, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &CA{
		Kind:    kind,
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		cert:    cert,
		key:     key,
	}, nil
}

func parseCert(certPEM []byte) (*x509.Certificate, error) {
	blk, _ := pem.Decode(certPEM)
	if blk == nil {
		return nil, fmt.Errorf("不是合法的 PEM 证书")
	}
	return x509.ParseCertificate(blk.Bytes)
}

func newSerial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("生成序列号: %w", err)
	}
	return n, nil
}
