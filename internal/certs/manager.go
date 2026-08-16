package certs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	// RenewBefore 是续期窗口：剩余寿命少于它就续。
	//
	// 取 30 天是 Let's Encrypt 的惯例：90 天有效期下，续期失败还有整整一个月
	// 可以人工介入。取太短会让一次持续几天的 DNS 故障直接演变成证书过期。
	RenewBefore = 30 * 24 * time.Hour

	// FailureBackoff 是一次失败之后的最短重试间隔。
	//
	// 巡检每小时一跑。失败了立刻重试的话，一个配错的凭据一天能撞 24 次，
	// 而 Let's Encrypt 对**失败校验**同样有速率限制（每小时 5 次）——
	// 撞进去之后连正确配置也签不出来。
	FailureBackoff = time.Hour

	// AlertBelowDays 是开始告警的剩余天数。
	AlertBelowDays = 14
)

// Issuer 签发一张证书。
//
// 做成接口是因为 ACME 那一步没法在开发机上验（要真实域名与 DNS 凭据）。
// 围绕它的调度逻辑才是会把速率配额烧光的地方，那部分必须能测。
type Issuer interface {
	Issue(ctx context.Context, domain string) (Cert, error)
}

// Alerts 接收证书相关的告警。
type Alerts interface {
	// CertAlert 的 kind 取 warn / crit，与事件流一致。
	CertAlert(kind, domain, msg string)
}

type ManagerDeps struct {
	Store  Store
	Master []byte
	Issuer Issuer
	Alerts Alerts
	Logger *slog.Logger
	// Now 可替换时钟，仅供测试注入。
	Now func() time.Time
}

type Manager struct {
	deps ManagerDeps
	log  *slog.Logger
	now  func() time.Time

	mu sync.Mutex
	// nextTry 记录每个域名最早可以再次尝试的时刻，用于退避。
	nextTry map[string]time.Time
}

func NewManager(d ManagerDeps) *Manager {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{deps: d, log: log, now: now, nextTry: map[string]time.Time{}}
}

// Ensure 保证给定域名都有可用证书：缺的签、快到期的续。
//
// **单个域名失败不让整轮报错**：一个域名的 DNS 配错了，不该连累其它域名
// 都不续期。失败的那个走告警。
func (m *Manager) Ensure(ctx context.Context, domains []string) error {
	for _, domain := range domains {
		m.ensureOne(ctx, domain)
	}
	return nil
}

func (m *Manager) ensureOne(ctx context.Context, domain string) {
	now := m.now()
	cur, err := Get(ctx, m.deps.Store, m.deps.Master, domain)

	switch {
	case errors.Is(err, ErrNotFound):
		// 没有证书，签一张
	case err != nil:
		// 解不开说明主密钥变了。**不能**当成「没有证书」去重签——
		// 那会把速率配额烧光，而问题其实出在配置上。
		m.log.Error("读取证书失败", "domain", domain, "err", err)
		m.alert("crit", domain, "读取证书失败："+err.Error())
		return
	default:
		left := cur.NotAfter.Sub(now)
		if left <= 0 {
			// 已经过期：说明之前的续期已经失败了一阵子
			m.alert("crit", domain, fmt.Sprintf("证书已于 %s 过期", cur.NotAfter.Format("2006-01-02")))
		} else if left < AlertBelowDays*24*time.Hour {
			m.alert("warn", domain, fmt.Sprintf("证书还有 %d 天到期", cur.DaysLeft(now)))
		}
		if !cur.Auto {
			// 手工导入的证书主控没有签发渠道，硬续只会失败，然后每轮告警一次，
			// 把真正需要注意的告警淹掉。到期提醒上面已经发过了。
			return
		}
		if left > RenewBefore {
			return
		}
	}

	if !m.mayTry(domain, now) {
		return
	}
	fresh, err := m.deps.Issuer.Issue(ctx, domain)
	if err != nil {
		// 旧证书**原样留着**：签失败就删掉的话，本来还能撑二十多天的证书
		// 也没了。告警是给人留出这二十多天的唯一途径。
		m.backoff(domain, now)
		m.log.Error("签发证书失败", "domain", domain, "err", err)
		m.alert("crit", domain, "签发失败："+err.Error())
		return
	}
	fresh.Domain = domain
	fresh.Auto = true
	if err := Save(ctx, m.deps.Store, m.deps.Master, fresh); err != nil {
		m.log.Error("保存证书失败", "domain", domain, "err", err)
		m.alert("crit", domain, "保存失败："+err.Error())
		return
	}
	m.clearBackoff(domain)
	m.log.Info("证书已签发", "domain", domain, "not_after", fresh.NotAfter)
}

func (m *Manager) mayTry(domain string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, held := m.nextTry[domain]
	return !held || !now.Before(next)
}

func (m *Manager) backoff(domain string, now time.Time) {
	m.mu.Lock()
	m.nextTry[domain] = now.Add(FailureBackoff)
	m.mu.Unlock()
}

func (m *Manager) clearBackoff(domain string) {
	m.mu.Lock()
	delete(m.nextTry, domain)
	m.mu.Unlock()
}

func (m *Manager) alert(kind, domain, msg string) {
	if m.deps.Alerts == nil {
		return
	}
	m.deps.Alerts.CertAlert(kind, domain, msg)
}

// Run 周期性巡检，直到 ctx 结束。domains 每轮重新取，新增域名不必重启。
func (m *Manager) Run(ctx context.Context, interval time.Duration, domains func(context.Context) ([]string, error)) {
	if interval <= 0 {
		interval = time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ds, err := domains(ctx)
			if err != nil {
				m.log.Error("读取待签发域名失败", "err", err)
				continue
			}
			_ = m.Ensure(ctx, ds)
		}
	}
}
