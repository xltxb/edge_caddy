package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
)

// TestGetCertAndListCertsAgreeOnFields：一条资源的两条读路径要给出同一份东西。
//
// GetCert 的 SELECT 比 ListCerts 少一个 expiry_alerted_at，于是它返回的
// Cert.ExpiryAlertedAt 恒为 nil——**读起来是「从没告警过」**（issue #80）。
//
// 今天没出事，只因为告警扫描走的是 ListCerts。哪天有人改用 GetCert，
// 症状是每天重复告警，而代码看着完全正常——domain.md 记过这个形状：
// 「一个字段退化成正常的样子，比退化成缺失更坏」。
func TestGetCertAndListCertsAgreeOnFields(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()
	sealer, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	const domain = "t.example.com"
	if err := st.PutCert(ctx, store.Cert{
		Domain: domain, Issuer: "Let's Encrypt", Challenge: "imported",
		CertPEM: []byte("cert"), KeyPEM: []byte("key"),
		NotAfter: time.Now().Add(30 * 24 * time.Hour),
	}, sealer); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkExpiryAlerted(ctx, domain); err != nil {
		t.Fatal(err)
	}

	one, err := st.GetCert(ctx, domain, sealer)
	if err != nil {
		t.Fatal(err)
	}
	list, err := st.ListCerts(ctx, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("装置：列表里有 %d 条", len(list))
	}

	if (one.ExpiryAlertedAt == nil) != (list[0].ExpiryAlertedAt == nil) {
		t.Errorf("GetCert 的 ExpiryAlertedAt 是 %v，ListCerts 的是 %v —— "+
			"两条读路径给出了不同的答案，而 nil 读起来是「从没告警过」",
			one.ExpiryAlertedAt, list[0].ExpiryAlertedAt)
	}
}
