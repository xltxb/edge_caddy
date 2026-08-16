package pki_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/pki"
)

func upstreamIssuer(t *testing.T, now func() time.Time) (*pki.UpstreamIssuer, *pki.CA) {
	t.Helper()
	ca, err := pki.NewCA("Edge Upstream CA")
	if err != nil {
		t.Fatal(err)
	}
	return pki.NewUpstreamIssuer(ca, now), ca
}

// 回源叶子的有效期是 24 小时（ADR-0009）。
//
// 做短是因为吊销靠过期：内部 PKI 的 CRL/OCSP 基本没人真部署，写了也是摆设。
// 把叶子做短，吊销就退化成「停止续期」这一个动作。
func TestUpstreamLeafIsShortLived(t *testing.T) {
	now := time.Now()
	iss, _ := upstreamIssuer(t, func() time.Time { return now })

	got, err := iss.EnsureFor(context.Background(), "node-hk-01")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(got.CertPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	life := cert.NotAfter.Sub(now)
	if life > 25*time.Hour || life < 23*time.Hour {
		t.Fatalf("回源叶子应约 24 小时有效，实际 %v", life)
	}
	if cert.Subject.CommonName != "node-hk-01" {
		t.Errorf("证书主体应为节点 ID，实际 %q", cert.Subject.CommonName)
	}
}

// 未到续期时点时复用同一张证书，不每次都重签。
//
// 每次都重签意味着每次下发都会把节点上的证书换掉，而换证书要重载 Caddy——
// 一次无关的配置下发会顺带打断所有回源连接。
func TestUpstreamCertIsReusedUntilRenewalWindow(t *testing.T) {
	now := time.Now()
	iss, _ := upstreamIssuer(t, func() time.Time { return now })
	ctx := context.Background()

	first, err := iss.EnsureFor(ctx, "node-hk-01")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	again, err := iss.EnsureFor(ctx, "node-hk-01")
	if err != nil {
		t.Fatal(err)
	}
	if string(first.CertPEM) != string(again.CertPEM) {
		t.Fatal("未到续期窗口不应重签——重签会让一次无关的下发打断所有回源连接")
	}
}

// 进入续期窗口后换新证书。
//
// 窗口取剩余寿命的三分之一：太晚续会让一次续期失败直接导致过期，
// 太早续等于把 24 小时的吊销窗口又拉长了。
func TestUpstreamCertRenewsBeforeExpiry(t *testing.T) {
	now := time.Now()
	iss, _ := upstreamIssuer(t, func() time.Time { return now })
	ctx := context.Background()

	first, err := iss.EnsureFor(ctx, "node-hk-01")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * time.Hour) // 只剩 4 小时
	renewed, err := iss.EnsureFor(ctx, "node-hk-01")
	if err != nil {
		t.Fatal(err)
	}
	if string(first.CertPEM) == string(renewed.CertPEM) {
		t.Fatal("临近到期应换新证书")
	}
}

// 每个节点各自一张证书，互不混用。
//
// 混用的话，吊销一个节点就等于吊销全部——而「只停掉这一台」正是吊销的意义。
func TestEachNodeGetsItsOwnCert(t *testing.T) {
	now := time.Now()
	iss, _ := upstreamIssuer(t, func() time.Time { return now })
	ctx := context.Background()

	a, err := iss.EnsureFor(ctx, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := iss.EnsureFor(ctx, "node-b")
	if err != nil {
		t.Fatal(err)
	}
	if string(a.CertPEM) == string(b.CertPEM) {
		t.Fatal("不同节点必须各持一张证书——混用会让吊销一个等于吊销全部")
	}
}

// 停止为某节点续期后，它的证书会自然过期。
func TestRevokeStopsRenewal(t *testing.T) {
	now := time.Now()
	iss, _ := upstreamIssuer(t, func() time.Time { return now })
	ctx := context.Background()

	if _, err := iss.EnsureFor(ctx, "node-bad"); err != nil {
		t.Fatal(err)
	}
	iss.Revoke("node-bad")
	if _, err := iss.EnsureFor(ctx, "node-bad"); err == nil {
		t.Fatal("已吊销的节点不应再签发证书")
	}
}
