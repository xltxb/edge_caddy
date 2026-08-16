package pki_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/store"
)

// 隧道 CA 与回源 CA 必须互不认可。
//
// 这条锁的是 ADR-0009：两者信任方不同（主控 vs 各源站），换根的影响面也不同。
// 共用一个根意味着源站侧的一次轮换会同时把所有节点踢下控制面——而那正是你最需要
// 控制面去下发新证书的时刻。这个循环依赖只能在设计阶段断开。
//
// 日后若有人把两套 CA 合并（"反正都是内部 PKI"），这条测试是唯一会报警的东西。
func TestTunnelAndUpstreamCAsDoNotTrustEachOther(t *testing.T) {
	tunnelCA := mustCA(t, "Edge Tunnel CA")
	upstreamCA := mustCA(t, "Edge Upstream CA")

	tunnelCert := mustIssue(t, tunnelCA, "node-hk-01", time.Hour)
	upstreamCert := mustIssue(t, upstreamCA, "node-hk-01", time.Hour)

	if err := tunnelCA.Verify(tunnelCert.CertPEM); err != nil {
		t.Fatalf("隧道 CA 应认可自己签的证书: %v", err)
	}
	if err := upstreamCA.Verify(upstreamCert.CertPEM); err != nil {
		t.Fatalf("回源 CA 应认可自己签的证书: %v", err)
	}

	if err := upstreamCA.Verify(tunnelCert.CertPEM); err == nil {
		t.Error("回源 CA 不应认可隧道 CA 签的证书——两套信任域必须隔离")
	}
	if err := tunnelCA.Verify(upstreamCert.CertPEM); err == nil {
		t.Error("隧道 CA 不应认可回源 CA 签的证书——两套信任域必须隔离")
	}
}

// 证书必须带上节点身份，否则主控无法从连接判断对面是谁。
func TestIssuedCertCarriesNodeIdentity(t *testing.T) {
	ca := mustCA(t, "Edge Tunnel CA")
	issued := mustIssue(t, ca, "node-hk-01", time.Hour)

	if got := issued.Subject; got != "node-hk-01" {
		t.Fatalf("证书主体应为节点 ID，实际 %q", got)
	}
}

func mustCA(t *testing.T, name string) *pki.CA {
	t.Helper()
	ca, err := pki.NewCA(name)
	if err != nil {
		t.Fatalf("创建 CA %q: %v", name, err)
	}
	return ca
}

func mustIssue(t *testing.T, ca *pki.CA, subject string, ttl time.Duration) pki.Issued {
	t.Helper()
	is, err := ca.IssueClient(subject, ttl)
	if err != nil {
		t.Fatalf("签发 %q: %v", subject, err)
	}
	return is
}

// CA 必须能用同一个主密钥重新加载出来，且**根证书不变**。
//
// 根变了等于所有已签发的凭据集体作废：节点全部失联、源站的信任库全部要换。
func TestCAReloadsWithSameRoot(t *testing.T) {
	st, ctx := newPKIStore(t), context.Background()
	secret := []byte("test-master-secret")

	first, err := pki.LoadOrCreate(ctx, st, pki.KeyTunnelCA, secret, "Edge Tunnel CA")
	if err != nil {
		t.Fatal(err)
	}
	again, err := pki.LoadOrCreate(ctx, st, pki.KeyTunnelCA, secret, "Edge Tunnel CA")
	if err != nil {
		t.Fatalf("同一主密钥应能重新加载: %v", err)
	}
	if string(first.RootPEM()) != string(again.RootPEM()) {
		t.Fatal("重新加载后根证书变了——所有已签发凭据会集体作废")
	}
	// 重载出来的 CA 必须真的能签，也就是私钥确实被还原了
	if _, err := again.IssueClient("node-hk-01", time.Hour); err != nil {
		t.Fatalf("重载后的 CA 应能签发: %v", err)
	}
}

// 主密钥不对时必须**报错**，绝不能悄悄新建一套 CA。
//
// 悄悄新建的现象是「节点莫名其妙全掉线了」——最难排查的那一类故障，
// 因为启动日志一切正常，看不出根已经换了。
func TestWrongSecretFailsLoudlyInsteadOfCreatingNewCA(t *testing.T) {
	st, ctx := newPKIStore(t), context.Background()
	original, err := pki.LoadOrCreate(ctx, st, pki.KeyTunnelCA, []byte("right-secret"), "Edge Tunnel CA")
	if err != nil {
		t.Fatal(err)
	}

	got, err := pki.LoadOrCreate(ctx, st, pki.KeyTunnelCA, []byte("wrong-secret"), "Edge Tunnel CA")
	if err == nil {
		if string(got.RootPEM()) == string(original.RootPEM()) {
			t.Fatal("不可能：错误的主密钥解出了原来的 CA")
		}
		t.Fatal("主密钥不对时悄悄新建了一套 CA——所有节点会莫名其妙集体失联")
	}
}

// 没有主密钥时拒绝启动，不做「先明文存着以后再加密」。
func TestRefusesToPersistWithoutSecret(t *testing.T) {
	st, ctx := newPKIStore(t), context.Background()
	if _, err := pki.LoadOrCreate(ctx, st, pki.KeyTunnelCA, nil, "Edge Tunnel CA"); !errors.Is(err, pki.ErrNoSecretKey) {
		t.Fatalf("无主密钥时应返回 ErrNoSecretKey，实际 %v", err)
	}
}

func newPKIStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "pki.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
