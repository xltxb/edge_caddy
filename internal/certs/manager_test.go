package certs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/certs"
	"github.com/xltxb/edge_caddy/internal/store"
)

// fakeIssuer 记录签发请求。ACME 那一步没法在开发机上真跑（要真实域名与
// DNS 凭据），因此把它做成可替换的边界——**围绕它的调度逻辑**才是这里要验的，
// 而那部分恰恰是会把速率配额烧光的地方。
type fakeIssuer struct {
	mu     sync.Mutex
	calls  []string
	fail   map[string]error
	expiry time.Time
	t      *testing.T
}

func (f *fakeIssuer) Issue(_ context.Context, domain string) (certs.Cert, error) {
	f.mu.Lock()
	f.calls = append(f.calls, domain)
	err := f.fail[domain]
	f.mu.Unlock()
	if err != nil {
		return certs.Cert{}, err
	}
	return selfSigned(f.t, domain, f.expiry), nil
}

func (f *fakeIssuer) issued() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

type fakeAlerts struct {
	mu   sync.Mutex
	msgs []string
}

func (a *fakeAlerts) CertAlert(kind, domain, msg string) {
	a.mu.Lock()
	a.msgs = append(a.msgs, kind+"|"+domain+"|"+msg)
	a.mu.Unlock()
}

func (a *fakeAlerts) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.msgs...)
}

func newManager(t *testing.T, now func() time.Time) (*certs.Manager, *store.Store, *fakeIssuer, *fakeAlerts) {
	t.Helper()
	st := newStore(t)
	iss := &fakeIssuer{t: t, fail: map[string]error{}, expiry: now().Add(90 * 24 * time.Hour)}
	al := &fakeAlerts{}
	m := certs.NewManager(certs.ManagerDeps{
		Store: st, Master: master, Issuer: iss, Alerts: al, Now: now,
	})
	return m, st, iss, al
}

// 没有证书的域名会被签发。
func TestEnsureIssuesMissingCert(t *testing.T) {
	now := time.Now()
	m, st, iss, _ := newManager(t, func() time.Time { return now })
	ctx := context.Background()

	if err := m.Ensure(ctx, []string{"api.example.com"}); err != nil {
		t.Fatal(err)
	}
	if got := iss.issued(); len(got) != 1 || got[0] != "api.example.com" {
		t.Fatalf("应签发一次，实际 %v", got)
	}
	if _, err := certs.Get(ctx, st, master, "api.example.com"); err != nil {
		t.Fatalf("签发后应已落库: %v", err)
	}
}

// **还早的证书绝不重签**。
//
// Let's Encrypt 对每个注册域名是每周 5 张证书。每次巡检都重签的话，
// 一天就把配额烧光，而后果是「续期全部失败」——恰恰发生在真到期的时候。
func TestEnsureSkipsCertsNotDueForRenewal(t *testing.T) {
	now := time.Now()
	m, st, iss, _ := newManager(t, func() time.Time { return now })
	ctx := context.Background()

	// 先放一张还有 80 天的
	if err := certs.Save(ctx, st, master,
		selfSigned(t, "api.example.com", now.Add(80*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := m.Ensure(ctx, []string{"api.example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := iss.issued(); len(got) != 0 {
		t.Fatalf("还有 80 天的证书不该重签，实际签了 %d 次——配额是每周 5 张", len(got))
	}
}

// 进入续期窗口就续。
func TestEnsureRenewsWhenNearExpiry(t *testing.T) {
	now := time.Now()
	m, st, iss, _ := newManager(t, func() time.Time { return now })
	ctx := context.Background()

	// 只剩 20 天：默认窗口是 30 天
	if err := certs.Save(ctx, st, master,
		selfSigned(t, "api.example.com", now.Add(20*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(ctx, []string{"api.example.com"}); err != nil {
		t.Fatal(err)
	}
	if got := iss.issued(); len(got) != 1 {
		t.Fatalf("进入续期窗口应续一次，实际 %d 次", len(got))
	}
	got, err := certs.Get(ctx, st, master, "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.DaysLeft(now) < 80 {
		t.Errorf("续期后应是新证书，实际还剩 %d 天", got.DaysLeft(now))
	}
}

// 续期失败时**保留旧证书**，并发一条告警。
//
// 把旧的删掉再签是最糟的顺序：签失败就什么都没有了，而旧证书本来还能再撑
// 二十多天。告警是给人留出这二十多天的唯一途径。
func TestRenewFailureKeepsOldCertAndAlerts(t *testing.T) {
	now := time.Now()
	m, st, iss, al := newManager(t, func() time.Time { return now })
	ctx := context.Background()

	old := selfSigned(t, "api.example.com", now.Add(20*24*time.Hour))
	if err := certs.Save(ctx, st, master, old); err != nil {
		t.Fatal(err)
	}
	iss.fail["api.example.com"] = errors.New("DNS 服务商拒绝：凭据无效")

	// 整体不算失败：一个域名续期失败不该让其它域名也不续
	if err := m.Ensure(ctx, []string{"api.example.com"}); err != nil {
		t.Fatalf("单个域名失败不该让整轮报错: %v", err)
	}

	got, err := certs.Get(ctx, st, master, "api.example.com")
	if err != nil {
		t.Fatalf("续期失败后旧证书必须还在: %v", err)
	}
	if got.Serial != old.Serial {
		t.Error("续期失败后不该动旧证书")
	}
	msgs := al.all()
	if len(msgs) == 0 {
		t.Fatal("续期失败必须告警——那是人还剩二十多天可以处理的唯一途径")
	}
	if !contains(msgs[0], "api.example.com") || !contains(msgs[0], "凭据无效") {
		t.Errorf("告警要说清楚哪个域名、什么原因，实际 %q", msgs[0])
	}
}

// 连续失败不会每轮都重试到把配额打光。
//
// 巡检是每小时一跑。失败了立刻重试的话，一个配错的凭据一天能撞 24 次，
// 而 Let's Encrypt 对失败校验同样有速率限制（每小时 5 次）——
// 撞进去之后连正确配置也签不出来了。
func TestFailureBacksOff(t *testing.T) {
	now := time.Now()
	m, st, iss, _ := newManager(t, func() time.Time { return now })
	ctx := context.Background()
	if err := certs.Save(ctx, st, master,
		selfSigned(t, "api.example.com", now.Add(20*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	iss.fail["api.example.com"] = errors.New("boom")

	for i := 0; i < 5; i++ {
		_ = m.Ensure(ctx, []string{"api.example.com"})
	}
	if n := len(iss.issued()); n != 1 {
		t.Fatalf("失败后应退避，5 轮只该尝试 1 次，实际 %d 次", n)
	}

	// 退避到期后再试
	now = now.Add(2 * time.Hour)
	_ = m.Ensure(ctx, []string{"api.example.com"})
	if n := len(iss.issued()); n != 2 {
		t.Fatalf("退避到期后应重试，实际共 %d 次", n)
	}
}

// 一个域名失败不影响其它域名。
func TestOneFailureDoesNotBlockOthers(t *testing.T) {
	now := time.Now()
	m, _, iss, _ := newManager(t, func() time.Time { return now })
	iss.fail["bad.example.com"] = errors.New("boom")

	if err := m.Ensure(context.Background(),
		[]string{"bad.example.com", "good.example.com"}); err != nil {
		t.Fatalf("单域名失败不该让整轮报错: %v", err)
	}
	if len(iss.issued()) != 2 {
		t.Fatalf("两个域名都该尝试，实际 %v", iss.issued())
	}
}

// 已经过期的证书要立刻续，而不是「等进入窗口」——它已经过窗口了。
func TestExpiredCertIsRenewedImmediately(t *testing.T) {
	now := time.Now()
	m, st, iss, al := newManager(t, func() time.Time { return now })
	ctx := context.Background()
	if err := certs.Save(ctx, st, master,
		selfSigned(t, "api.example.com", now.Add(-24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(ctx, []string{"api.example.com"}); err != nil {
		t.Fatal(err)
	}
	if len(iss.issued()) != 1 {
		t.Fatal("已过期的证书必须立刻续")
	}
	// 已经过期本身就该告警：它意味着之前的续期已经失败了一段时间
	if len(al.all()) == 0 {
		t.Error("证书已过期应告警——它说明之前的续期已经失败了一阵子")
	}
}

// 手工导入的证书不自动续期。
//
// 主控没有它的签发渠道，硬续只会失败，然后每轮告警一次——
// 把真正需要注意的告警淹掉。
func TestManualCertsAreNotAutoRenewed(t *testing.T) {
	now := time.Now()
	m, st, iss, _ := newManager(t, func() time.Time { return now })
	ctx := context.Background()

	c := selfSigned(t, "manual.example.com", now.Add(5*24*time.Hour))
	c.Auto = false
	if err := certs.Save(ctx, st, master, c); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(ctx, []string{"manual.example.com"}); err != nil {
		t.Fatal(err)
	}
	if n := len(iss.issued()); n != 0 {
		t.Fatalf("手工导入的证书不该自动续期，实际签了 %d 次", n)
	}
}

// 手工证书临近到期仍要告警：不自动续不等于不用管。
func TestManualCertNearExpiryStillAlerts(t *testing.T) {
	now := time.Now()
	m, st, _, al := newManager(t, func() time.Time { return now })
	ctx := context.Background()

	c := selfSigned(t, "manual.example.com", now.Add(5*24*time.Hour))
	c.Auto = false
	if err := certs.Save(ctx, st, master, c); err != nil {
		t.Fatal(err)
	}
	_ = m.Ensure(ctx, []string{"manual.example.com"})
	if len(al.all()) == 0 {
		t.Fatal("手工证书临近到期必须告警——不自动续不等于不用管")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
