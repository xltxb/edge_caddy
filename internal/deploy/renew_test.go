package deploy_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

// TestUpstreamCertsRenewWithNobodyDeploying：**没有人点任何东西，回源证书照样换新的。**
//
// ADR-0009 把回源叶子定成 24 小时，续期通道写的是「gRPC 隧道，自动」，
// 并且把失效条件说死在**失联**超过 24 小时上：「一台你整天联系不上的机器，
// 不该继续拿着凭据进你的源站。」
//
// 而续期原先只挂在下发路径上（`upstreamCertFor` 的三个调用点全在那儿），
// 于是真实条件是「**没人点下发**超过 24 小时」——两件完全不同的事。一个配置
// 稳定、隧道健康、指标全绿的集群会在最后一次下发满 24 小时之后，所有 mtls
// 路由的回源开始被源站拒绝，而主控侧一点症状都没有（issue #39）。
//
// 所以这条测试先跑一次**正常下发**把基线立起来，然后 `forget()`——
// 从那一刻起再没有人碰过这个系统。之后发生的每一次推送都不是人点出来的。
func TestUpstreamCertsRenewWithNobodyDeploying(t *testing.T) {
	p := newFakePusher("hk-01", "tw-01")
	s, _ := newSched(t, p)

	ca, err := pki.GenerateCA(pki.KindUpstream)
	if err != nil {
		t.Fatal(err)
	}
	s.UpstreamCA = ca
	s.Render.UpstreamClientCert = "/etc/caddy/upstream/client.crt"
	s.Render.UpstreamClientKey = "/etc/caddy/upstream/client.key"

	ctx := context.Background()
	if _, _, err := s.Deploy(ctx, "abiu", []string{"route:api.example.com"}); err != nil {
		t.Fatal(err)
	}

	// 记下下发那一刻发下去的是哪张证书。**续期的判据是「换了一张」，
	// 不是「有一张」**——只断言「收到了一张 24 小时的证书」的话，一张下发时
	// 就签好、原样躺在那里的旧证书同样满足它，而那正是这个 bug 的样子。
	atDeploy := map[string]string{}
	for _, node := range []string{"hk-01", "tw-01"} {
		ups := p.upstreamCertsFor(node)
		if len(ups) == 0 {
			t.Fatalf("下发时 %s 没收到回源证书，这条测试的前提就不成立", node)
		}
		atDeploy[node] = parseLeaf(t, ups[0].CertPEM).SerialNumber.String()
	}
	p.forget()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// 周期做成参数，理由与 RetryBackoff 那个字段一样：真等 8 小时的测试没有人会跑。
	go s.RunUpstreamRenewal(runCtx, 5*time.Millisecond)

	for _, node := range []string{"hk-01", "tw-01"} {
		up := waitForUpstreamCert(t, p, node)
		leaf := parseLeaf(t, up.CertPEM)

		if leaf.SerialNumber.String() == atDeploy[node] {
			t.Errorf("%s 收到的还是下发时那张证书（序列号 %s）—— 没有续期，只是又推了一遍",
				node, atDeploy[node])
		}
		if leaf.Subject.CommonName != node {
			t.Errorf("%s 收到的证书 CN 是 %q —— 主控据 CN 认出对端是谁", node, leaf.Subject.CommonName)
		}
		// 判据取自 ADR-0009 的表格，不是从代码里抄回来的那个字面量。
		if d := time.Until(leaf.NotAfter); d > 25*time.Hour || d < 23*time.Hour {
			t.Errorf("%s 的回源叶子还有 %v 到期，ADR-0009 定的是 24 小时", node, d)
		}
		if len(up.KeyPEM) == 0 {
			t.Errorf("%s 只收到证书没收到私钥，那张证书用不了", node)
		}
		if up.CertPath != s.Render.UpstreamClientCert {
			t.Errorf("%s 的证书落盘路径是 %q，想要 %q", node, up.CertPath, s.Render.UpstreamClientCert)
		}
	}
}

// TestUpstreamRenewalStaysPutWithoutAnUpstreamCA：没配回源 CA 时续期循环什么都不做。
//
// 没有 CA 就没有要续的东西，而一个照样按周期推全网的循环会：每 8 小时让每台
// 节点热重载一次配置、并在下发记录里留下没人点过的行。**空转要真的空**。
func TestUpstreamRenewalStaysPutWithoutAnUpstreamCA(t *testing.T) {
	p := newFakePusher("hk-01")
	s, _ := newSched(t, p)
	// 刻意不设 UpstreamCA / UpstreamClientCert。

	ctx := context.Background()
	if _, _, err := s.Deploy(ctx, "abiu", []string{"route:api.example.com"}); err != nil {
		t.Fatal(err)
	}
	p.forget()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.RunUpstreamRenewal(runCtx, time.Millisecond)

	// 给它足够多个周期去犯错。
	time.Sleep(50 * time.Millisecond)
	if n := p.attemptsFor("hk-01"); n != 0 {
		t.Errorf("没配回源 CA，却推了 %d 次——这些热重载没有任何理由", n)
	}
}

func waitForUpstreamCert(t *testing.T, p *fakePusher, node string) tunnel.UpstreamCert {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, up := range p.upstreamCertsFor(node) {
			if len(up.CertPEM) > 0 {
				return up
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等了 2 秒，%s 一张回源证书都没收到", node)
	return tunnel.UpstreamCert{}
}

func parseLeaf(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	blk, _ := pem.Decode(certPEM)
	if blk == nil {
		t.Fatal("证书不是合法的 PEM")
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("解析证书: %v", err)
	}
	return leaf
}
