package certs_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/certs"
)

// mint 造一张由「外部平台 CA」签出来的叶子证书。
//
// **不用自签。** 第一版用同一个模板当 template 和 parent，
// 于是 issuer 被标准库正确地填成了叶子自己的 CN——测试断言
// 「签发者是外部平台 CA」当场红了。那是**我的假设错了，不是代码错了**：
// 自签证书的签发者就是它自己。
//
// 造一条真链更接近外部平台给的东西，而且让「有没有中间证书」这一支也能测。
func mint(t *testing.T, opts func(*x509.Certificate)) (certPEM, keyPEM []byte) {
	t.Helper()
	caCert, caKey := mintCA(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "api.example.com"},
		DNSNames:     []string{"api.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}
	if opts != nil {
		opts(tpl)
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

// mintChain 造「叶子 + 中间证书」，也就是外部平台该给的那种完整链。
func mintChain(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	caCert, caKey := mintCA(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "api.example.com"},
		DNSNames:     []string{"api.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	inter := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
	return append(leaf, inter...),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

func mintCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "外部证书平台 CA"},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c, key
}

func TestImportAcceptsAGoodCert(t *testing.T) {
	certPEM, keyPEM := mint(t, nil)
	imp, err := certs.ValidateImport("api.example.com", certPEM, keyPEM)
	if err != nil {
		t.Fatalf("这张证书应当能导入：%v", err)
	}
	if imp.Issuer != "外部证书平台 CA" {
		t.Errorf("签发者 = %q", imp.Issuer)
	}
	if time.Until(imp.NotAfter) < 80*24*time.Hour {
		t.Errorf("到期时间没读对：%v", imp.NotAfter)
	}
	// 只有叶子没有中间证书 —— 该警告，但**不该拒绝**。
	if len(imp.Warnings) == 0 {
		t.Error("只有叶子证书时应当给出提示")
	}
}

// **私钥配不上证书。**
//
// 两边单独看都是合法 PEM，存库成功、下发成功、界面显示「已导入」，
// 而 TLS 握手会失败——这类失败不会当场显形，所以必须在存之前挡住。
func TestImportRejectsMismatchedKey(t *testing.T) {
	certPEM, _ := mint(t, nil)
	_, otherKey := mint(t, nil) // 另一张证书的私钥

	_, err := certs.ValidateImport("api.example.com", certPEM, otherKey)
	if err == nil {
		t.Fatal("私钥配不上证书时必须拒绝")
	}
	if !strings.Contains(err.Error(), "不匹配") {
		t.Errorf("理由要说清是配不上，而不是格式错：%v", err)
	}
}

// **证书不覆盖那个域名。**
//
// 证书本身完全有效，只是签的是别的域名——浏览器会报名称不符，
// 而人会去查 DNS、查 Caddy 配置，因为「证书是有效的」。
func TestImportRejectsWrongDomain(t *testing.T) {
	certPEM, keyPEM := mint(t, func(c *x509.Certificate) {
		c.Subject.CommonName = "other.example.com"
		c.DNSNames = []string{"other.example.com"}
	})
	_, err := certs.ValidateImport("api.example.com", certPEM, keyPEM)
	if err == nil {
		t.Fatal("证书不覆盖那个域名时必须拒绝")
	}
	// 报错要说出**它实际覆盖的是什么** —— 人多半是传错了文件，
	// 那一行能让他当场认出来。
	if !strings.Contains(err.Error(), "other.example.com") {
		t.Errorf("要说出它实际覆盖的域名：%v", err)
	}
}

// 通配符证书要按 RFC 匹配，不能用字符串比对。
//
// 手写 strings.HasSuffix 会在 `*.example.com` 上判错，
// **而通配符正是外部平台最常见的一类证书**。
func TestImportAcceptsWildcard(t *testing.T) {
	certPEM, keyPEM := mint(t, func(c *x509.Certificate) {
		c.Subject.CommonName = "*.example.com"
		c.DNSNames = []string{"*.example.com"}
	})
	if _, err := certs.ValidateImport("api.example.com", certPEM, keyPEM); err != nil {
		t.Fatalf("通配符证书应当覆盖 api.example.com：%v", err)
	}
	// 而它不该覆盖多级子域（RFC 6125：通配符只匹配一层）。
	if _, err := certs.ValidateImport("a.b.example.com", certPEM, keyPEM); err == nil {
		t.Error("*.example.com 不该覆盖 a.b.example.com")
	}
}

// **已过期的证书。**
//
// 导入进来界面照样显示「已导入」，而它一路走到节点上。
func TestImportRejectsExpired(t *testing.T) {
	certPEM, keyPEM := mint(t, func(c *x509.Certificate) {
		c.NotBefore = time.Now().Add(-48 * time.Hour)
		c.NotAfter = time.Now().Add(-time.Hour)
	})
	_, err := certs.ValidateImport("api.example.com", certPEM, keyPEM)
	if err == nil {
		t.Fatal("过期的证书必须拒绝")
	}
	if !strings.Contains(err.Error(), "过期") {
		t.Errorf("理由要说清：%v", err)
	}
}

// 还没生效的证书（NotBefore 在未来）同样拒绝 —— 那多半是机器时钟不对，
// 而那件事值得当场知道。
func TestImportRejectsNotYetValid(t *testing.T) {
	certPEM, keyPEM := mint(t, func(c *x509.Certificate) {
		c.NotBefore = time.Now().Add(48 * time.Hour)
		c.NotAfter = time.Now().Add(90 * 24 * time.Hour)
	})
	if _, err := certs.ValidateImport("api.example.com", certPEM, keyPEM); err == nil {
		t.Fatal("还没生效的证书必须拒绝")
	}
}

// 快到期的证书**不拒绝，但要提醒**：导入的证书主控不会自动续期。
func TestImportWarnsAboutSoonExpiring(t *testing.T) {
	certPEM, keyPEM := mint(t, func(c *x509.Certificate) {
		c.NotAfter = time.Now().Add(5 * 24 * time.Hour)
	})
	imp, err := certs.ValidateImport("api.example.com", certPEM, keyPEM)
	if err != nil {
		t.Fatalf("快到期不该拒绝：%v", err)
	}
	var saw bool
	for _, w := range imp.Warnings {
		if strings.Contains(w, "不会自动续期") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("要提醒它不会自动续期：%v", imp.Warnings)
	}
}

// 不是 PEM 的东西直接拒绝。
func TestImportRejectsGarbage(t *testing.T) {
	if _, err := certs.ValidateImport("api.example.com",
		[]byte("这不是证书"), []byte("这不是私钥")); err == nil {
		t.Fatal("非 PEM 内容必须拒绝")
	}
}

// 带中间证书的完整链**不该有那条「只有叶子」的提示**。
//
// 一条恒定出现的提示等于没有提示：人读第二遍就开始跳过它，
// 而它要提醒的那件事（部分客户端会握手失败）恰恰值得被读到。
func TestImportDoesNotWarnWhenChainIsComplete(t *testing.T) {
	certPEM, keyPEM := mintChain(t)
	imp, err := certs.ValidateImport("api.example.com", certPEM, keyPEM)
	if err != nil {
		t.Fatalf("完整链应当能导入：%v", err)
	}
	for _, w := range imp.Warnings {
		if strings.Contains(w, "只有叶子") {
			t.Errorf("链是完整的，不该提示缺中间证书：%v", imp.Warnings)
		}
	}
}
