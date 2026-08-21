// Package certs 是主控侧的证书签发与轮换。
//
// **证书由主控集中签发**（ADR-0001）：边缘节点跑 apt 装的官方 Caddy，
// 而官方包不含任何 DNS provider（caddy-dns/* 全是插件），也就做不了 DNS-01；
// 退到 HTTP-01 在这个系统里同样不成立——域名按权重只解析到部分节点，
// 轮换外的节点完不成校验，而节点恰恰需要在**进入轮换之前**就持有证书。
//
// 签发结果经 gRPC 隧道内联下发（ADR-0010），DNS 服务商凭据只存在于主控一处。
package certs

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// RenewBefore 是提前多久续期。
//
// Let's Encrypt 签 90 天，30 天的余量意味着连续失败一个月才会真的过期——
// 而那期间每天都有一次告警。留得更短会让一次周末的服务商故障变成事故。
const RenewBefore = 30 * 24 * time.Hour

// Issuer 是签发的出口。抽成接口是因为它是这套东西里**唯一无法在本地验证**
// 的部分：真 ACME 需要一个公网可解析的域名与一个真实的服务商账号，
// 而且会在 CA 那边留下真实记录。其余部分（下发、加载、回执）都被真 Caddy 验过。
type Issuer interface {
	// Issue 为一个域名签发证书，返回 PEM 与到期时间。
	Issue(ctx context.Context, domain string) (certPEM, keyPEM []byte, notAfter time.Time, err error)
	// Name 是签发者名字，写进证书页的「签发者」列。
	Name() string
}

// Redeployer 在证书变化后把新证书推到节点上。
//
// 证书随**每次下发**内联带上（ADR-0010），所以续期之后必须触发一次下发——
// 否则新证书会躺在主控库里，直到下一次有人改配置才下去，
// 而那期间节点上跑的还是旧的。
type Redeployer func(ctx context.Context, reason string) error

type Manager struct {
	Store    *store.Store
	Sealer   *secret.Sealer
	Issuer   Issuer
	Hub      *ws.Hub
	Log      *slog.Logger
	Redeploy Redeployer

	mu       sync.Mutex
	inFlight map[string]bool
}

func New(m *Manager) *Manager {
	if m.Log == nil {
		m.Log = slog.Default()
	}
	m.inFlight = map[string]bool{}
	return m
}

// RenewAsync 异步续期一个域名。ACME 要跟服务商往返，同步等会把 HTTP 请求拖很久。
func (m *Manager) RenewAsync(domain string) {
	m.mu.Lock()
	if m.inFlight[domain] {
		// 同一个域名不并发签两次：ACME 有速率限制，重复请求会把配额烧掉，
		// 而配额耗尽的表现是「一周内都签不出证书」。
		m.mu.Unlock()
		return
	}
	m.inFlight[domain] = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.inFlight, domain)
			m.mu.Unlock()
		}()
		ctx := context.Background()
		if err := m.renew(ctx, domain); err != nil {
			m.Log.Error("续期失败", "domain", domain, "err", err)
			m.event(ctx, "crit", fmt.Sprintf("证书 %s 续期失败：%v", domain, err))
			return
		}
		m.event(ctx, "ok", fmt.Sprintf("证书 %s 已续期", domain))
		if m.Redeploy != nil {
			if err := m.Redeploy(ctx, "证书续期"); err != nil {
				// 签下来了但没推下去：新证书躺在主控库里，节点上还是旧的。
				// 这必须说出来，否则证书页会显示「已续期」而节点上没变。
				m.Log.Error("续期后下发失败", "domain", domain, "err", err)
				m.event(ctx, "warn",
					fmt.Sprintf("证书 %s 已续期，但下发失败，节点上仍是旧证书：%v", domain, err))
			}
		}
	}()
}

// RenewDueAsync 把所有快到期的证书排进续期，返回排了几个。
func (m *Manager) RenewDueAsync() int {
	ctx := context.Background()
	due, err := m.due(ctx)
	if err != nil {
		m.Log.Error("检查到期证书失败", "err", err)
		return 0
	}
	for _, d := range due {
		m.RenewAsync(d)
	}
	return len(due)
}

func (m *Manager) due(ctx context.Context) ([]string, error) {
	list, err := m.Store.ListCerts(ctx, nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range list {
		if !c.AutoRenew {
			continue
		}
		if time.Until(c.NotAfter) < RenewBefore {
			out = append(out, c.Domain)
		}
	}
	return out, nil
}

// EnsureFor 给还没有证书的域名签发。由下发流水线在新增路由后调用。
func (m *Manager) EnsureFor(ctx context.Context, domains []string) {
	for _, d := range domains {
		if _, err := m.Store.GetCert(ctx, d, nil); err == nil {
			continue
		}
		m.RenewAsync(d)
	}
}

func (m *Manager) renew(ctx context.Context, domain string) error {
	if m.Issuer == nil {
		return fmt.Errorf("尚未配置证书签发（缺 ACME 账户或 DNS 服务商）")
	}
	if m.Sealer == nil {
		return fmt.Errorf("没有可用的密封器，无法保存私钥（装配漏了 Sealer）")
	}

	certPEM, keyPEM, notAfter, err := m.Issuer.Issue(ctx, domain)
	if err != nil {
		return err
	}
	if notAfter.IsZero() {
		// 签发者没给到期时间就从证书里读——到期时间是续期调度的唯一依据，
		// 取不到会让这张证书永远不被续。
		if notAfter, err = notAfterOf(certPEM); err != nil {
			return fmt.Errorf("读取到期时间: %w", err)
		}
	}

	return m.Store.PutCert(ctx, store.Cert{
		Domain: domain, Issuer: m.Issuer.Name(), Challenge: "dns-01",
		AutoRenew: true, CertPEM: certPEM, KeyPEM: keyPEM, NotAfter: notAfter,
	}, m.Sealer)
}

func notAfterOf(certPEM []byte) (time.Time, error) {
	blk, _ := pem.Decode(certPEM)
	if blk == nil {
		return time.Time{}, fmt.Errorf("不是合法的 PEM 证书")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return c.NotAfter, nil
}

// Run 每天查一次到期。
func (m *Manager) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 12 * time.Hour
	}
	t := time.NewTicker(every)
	defer t.Stop()
	m.RenewDueAsync() // 启动时先查一次：主控可能停了很久
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.RenewDueAsync()
		}
	}
}

func (m *Manager) event(ctx context.Context, kind, msg string) {
	e, err := m.Store.InsertEvent(ctx, "", kind, msg)
	if err != nil {
		m.Log.Error("写事件失败", "err", err)
		return
	}
	if m.Hub != nil {
		m.Hub.Broadcast(ws.TypeEvent, ws.Event{
			ID: e.ID, At: e.CreatedAt.Format(time.RFC3339),
			Node: ws.NodeRef(e.Node), Kind: e.Kind, Msg: e.Msg,
		})
	}
}
