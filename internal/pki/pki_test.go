package pki_test

import (
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/pki"
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
