package certs_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/certs"
	"github.com/xltxb/edge_caddy/internal/store"
)

var master = []byte("test-master-key")

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// selfSigned 造一张真证书。用真的 x509 而不是随便一串字节：
// 存取这一层的价值就在于「存进去的是证书、取出来还能解析」。
func selfSigned(t *testing.T, domain string, notAfter time.Time) certs.Cert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return certs.Cert{
		Domain:   domain,
		CertPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:   pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		NotAfter: notAfter.UTC().Truncate(time.Second),
		Issuer:   "Test CA",
		KeyType:  "ECDSA P-256",
		Serial:   tmpl.SerialNumber.Text(16),
		// 默认自动续期。不设的话零值是 false，会被当成手工导入的证书，
		// 而那条路径是不续期的——测试数据的默认值必须是常见情形。
		Auto: true,
	}
}

// 存进去再取出来，内容一致。
func TestCertRoundTrips(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	want := selfSigned(t, "api.example.com", time.Now().Add(60*24*time.Hour))

	if err := certs.Save(ctx, st, master, want); err != nil {
		t.Fatal(err)
	}
	got, err := certs.Get(ctx, st, master, "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.CertPEM) != string(want.CertPEM) {
		t.Error("证书内容不一致")
	}
	if string(got.KeyPEM) != string(want.KeyPEM) {
		t.Error("私钥内容不一致")
	}
	if !got.NotAfter.Equal(want.NotAfter) {
		t.Errorf("到期时间不一致：存 %v 取 %v", want.NotAfter, got.NotAfter)
	}
	if got.Issuer != want.Issuer || got.KeyType != want.KeyType {
		t.Errorf("元信息不一致：%+v", got)
	}
}

// **私钥在库里是密文**。
//
// 证书本身是公开的，私钥不是：拿到它就能冒充这个域名。明文躺在 SQLite 文件里，
// 一次备份、一次误传就出去了，而不会有任何东西提醒你。
func TestPrivateKeyIsEncryptedAtRest(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	c := selfSigned(t, "api.example.com", time.Now().Add(60*24*time.Hour))
	if err := certs.Save(ctx, st, master, c); err != nil {
		t.Fatal(err)
	}

	var blob []byte
	err := st.DB().QueryRowContext(ctx,
		`SELECT key_pem FROM certificates WHERE domain = ?`, "api.example.com").Scan(&blob)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "PRIVATE KEY") {
		t.Fatal("私钥以明文存进了库里")
	}
	// 证书本身不加密：它是公开信息，加密只会让排查时看不到内容
	var certBlob []byte
	if err := st.DB().QueryRowContext(ctx,
		`SELECT cert_pem FROM certificates WHERE domain = ?`, "api.example.com").Scan(&certBlob); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(certBlob), "CERTIFICATE") {
		t.Error("证书本身是公开信息，不必加密——加密只会让排查时看不到内容")
	}
}

// 主密钥不对时报错，不能当成「还没签过」返回空。
//
// 返回空会让续期逻辑以为没有证书，于是重新签一张——而 Let's Encrypt 的
// 速率限制是每个域名每周 5 张，几次重启就把配额烧光了。
func TestWrongMasterKeyIsAnError(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := certs.Save(ctx, st, master, selfSigned(t, "a.example.com", time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := certs.Get(ctx, st, []byte("wrong"), "a.example.com"); err == nil {
		t.Fatal("主密钥不对时必须报错")
	}
}

// 覆盖同一域名时是替换，不是追加——续期后只该有一张。
func TestSaveReplacesExisting(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	old := selfSigned(t, "api.example.com", time.Now().Add(10*24*time.Hour))
	fresh := selfSigned(t, "api.example.com", time.Now().Add(90*24*time.Hour))

	if err := certs.Save(ctx, st, master, old); err != nil {
		t.Fatal(err)
	}
	if err := certs.Save(ctx, st, master, fresh); err != nil {
		t.Fatal(err)
	}
	all, err := certs.All(ctx, st, master)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("同一域名应只有一张证书，实际 %d 张", len(all))
	}
	if !all[0].NotAfter.Equal(fresh.NotAfter) {
		t.Error("续期后应是新证书")
	}
}

// 没有的域名返回 ErrNotFound，而不是一张空证书。
//
// 空证书会被下发下去，Caddy 加载一个空 PEM 会拒绝整份 tls 配置——
// 而现象是「下发失败」，跟「这个域名还没签证书」差得很远。
func TestGetMissingReturnsNotFound(t *testing.T) {
	_, err := certs.Get(context.Background(), newStore(t), master, "nope.example.com")
	if err == nil {
		t.Fatal("不存在的域名应报 not found")
	}
}

// All 按到期时间升序：最紧急的排最前面。
func TestAllSortedByExpiry(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	for _, c := range []struct {
		domain string
		days   int
	}{{"c.example.com", 80}, {"a.example.com", 5}, {"b.example.com", 30}} {
		if err := certs.Save(ctx, st, master,
			selfSigned(t, c.domain, time.Now().Add(time.Duration(c.days)*24*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	all, err := certs.All(ctx, st, master)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{all[0].Domain, all[1].Domain, all[2].Domain}
	want := []string{"a.example.com", "b.example.com", "c.example.com"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("应按到期时间升序（最紧急的在前），实际 %v", got)
		}
	}
}

// 元信息可以从证书本身解出来，不必靠调用方传对。
//
// 靠调用方传的话，签发路径和导入路径会各填各的，而「签发者」这类字段
// 填错了没人看得出来。
func TestParseDerivesMetadataFromCert(t *testing.T) {
	want := selfSigned(t, "api.example.com", time.Now().Add(45*24*time.Hour))
	got, err := certs.Parse("api.example.com", want.CertPEM, want.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NotAfter.Equal(want.NotAfter) {
		t.Errorf("到期时间应从证书解出，实际 %v", got.NotAfter)
	}
	if got.KeyType == "" {
		t.Error("密钥类型应从证书解出")
	}
	if got.Issuer == "" {
		t.Error("签发者应从证书解出")
	}
	if got.Serial == "" {
		t.Error("序列号应从证书解出——换证时它是唯一能确认「真的换了」的字段")
	}
}

// 坏的 PEM 当场拒绝，不要等到下发到节点上才失败。
func TestParseRejectsGarbage(t *testing.T) {
	if _, err := certs.Parse("a.example.com", []byte("not a pem"), []byte("neither")); err == nil {
		t.Fatal("坏的 PEM 应当场拒绝")
	}
}
