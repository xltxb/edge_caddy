package certs_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/certs"
	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
)

type recAlert struct {
	mu   sync.Mutex
	sent []string
}

func (a *recAlert) Notify(_ context.Context, level, title, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, level+"|"+title)
}
func (a *recAlert) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.sent...)
}

func newMgr(t *testing.T) (*certs.Manager, *store.Store, *recAlert) {
	t.Helper()
	st := testdb.New(t)
	sealer, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	a := &recAlert{}
	return certs.New(&certs.Manager{Store: st, Sealer: sealer, Alert: a}), st, a
}

func putCert(t *testing.T, st *store.Store, domain string, notAfter time.Time) {
	t.Helper()
	sealer, _ := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err := st.PutCert(context.Background(), store.Cert{
		Domain: domain, Issuer: "外部平台", Challenge: "imported",
		AutoRenew: false, CertPEM: []byte("x"), KeyPEM: []byte("y"),
		NotAfter: notAfter,
	}, sealer); err != nil {
		t.Fatal(err)
	}
}

// **拆掉自动续期之后，到期扫描是「证书要过期了」唯一会主动找人的地方。**
//
// 两档分开是有意的：30 天是「安排一下」，14 天是「今天就得做」。
// 合成一档等于把这两句说成同一句，于是前一种会被当成后一种忽略掉——
// **而那正好训练人在真的来不及时也忽略。**
func TestExpiryScanAlertsOnlyWhenItShould(t *testing.T) {
	m, st, a := newMgr(t)
	ctx := context.Background()

	putCert(t, st, "fresh.example.com", time.Now().Add(80*24*time.Hour)) // 还早
	putCert(t, st, "soon.example.com", time.Now().Add(20*24*time.Hour))  // 黄档
	putCert(t, st, "urgent.example.com", time.Now().Add(5*24*time.Hour)) // 红档
	putCert(t, st, "dead.example.com", time.Now().Add(-2*24*time.Hour))  // 已过期

	warn, urgent, expired := m.ScanExpiry(ctx)
	if warn != 1 || urgent != 1 || expired != 1 {
		t.Fatalf("计数 = 黄%d 红%d 过期%d，想要 1/1/1", warn, urgent, expired)
	}

	sent := a.all()
	joined := strings.Join(sent, "\n")

	// **黄档不发告警。** 一条 30 天就响的告警，会让这一类告警在人心里
	// 一路贬值到 14 天那档也不响为止。
	if strings.Contains(joined, "soon.example.com") {
		t.Errorf("30 天那档不该发告警：%v", sent)
	}
	// 还早的那张更不该出现。
	if strings.Contains(joined, "fresh.example.com") {
		t.Errorf("80 天的证书不该发告警：%v", sent)
	}
	// 红档与已过期都要发，而**级别不同**：一个是「今天就得做」，
	// 一个是「此刻站点就是坏的」。
	if !strings.Contains(joined, "warn|证书将到期 urgent.example.com") {
		t.Errorf("14 天内该发 warn：%v", sent)
	}
	if !strings.Contains(joined, "crit|证书将到期 dead.example.com") {
		t.Errorf("已过期该发 crit：%v", sent)
	}
}

// **同一张证书一天最多报一次。**
//
// 扫描每天跑一次，所以这条平时不起作用——它挡的是主控重启，而重启是例行
// 操作（灰度上一晚部署了六七次）。不挡的话同一张证书会被报六次，
// 而**一个重启就重复报警的系统会教会人忽略那一类告警**，
// 下次真有证书要过期时他们照旧会忽略。
func TestExpiryAlertIsNotRepeatedOnRestart(t *testing.T) {
	m, st, a := newMgr(t)
	ctx := context.Background()
	putCert(t, st, "urgent.example.com", time.Now().Add(5*24*time.Hour))

	m.ScanExpiry(ctx)
	first := len(a.all())
	if first != 1 {
		t.Fatalf("第一次扫描该发 1 条，实际 %d", first)
	}

	// 再扫几遍 —— 相当于主控又重启了几次。
	for i := 0; i < 3; i++ {
		m.ScanExpiry(ctx)
	}
	if n := len(a.all()); n != first {
		t.Errorf("重复扫描不该重复报警，实际发了 %d 条：%v", n, a.all())
	}

	// **反过来：标记过期之后要能再报。** 没有这一条，一个「只报一次、
	// 此后永不再报」的实现也能让上面通过 —— 而那意味着证书在最后 14 天里
	// 只被提醒过一次，之后就再也没人提。
	if _, err := st.Pool.Exec(ctx,
		`UPDATE certs SET expiry_alerted_at = now() - interval '2 days'`); err != nil {
		t.Fatal(err)
	}
	m.ScanExpiry(ctx)
	if n := len(a.all()); n != first+1 {
		t.Errorf("隔了一天该再报一次，实际总共 %d 条", n)
	}
}
