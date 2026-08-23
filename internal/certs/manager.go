// Package certs 是主控侧的证书管理。
//
// **主控不签发证书。** 证书由外部证书平台签好之后经 `PUT /certs/:domain`
// 推进来，主控只负责三件事：存、经隧道内联下发到节点（ADR-0010）、
// 在快到期时把这件事说出来。
//
// 这推翻了 ADR-0001 的核心决定（「主控用 DNS-01 集中签发」），
// 理由见 ADR-0015。而 ADR-0001 里那条**没有**被推翻的部分仍然成立：
// 节点跑 apt 装的官方 Caddy、不持有 DNS 凭据、不自己申请证书。
//
// # 拆掉自动续期之后，到期提醒是唯一的防线
//
// 导入的证书没有任何东西会自动续。所以这个包里最要紧的不再是签发，
// 是 ScanExpiry —— **它是「证书到期」这件事唯一会主动找人的地方**。
package certs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// 到期的两个档位。**分两档是有意的。**
//
//	剩 WarnBefore  「安排一下」——界面标黄，不发告警
//	剩 AlertBefore 「今天就得做」——界面标红，每天一条告警
//
// 合成一档的代价是：把「还早」和「来不及了」说成同一句话，
// 于是前一种会被当成后一种忽略掉——而那正好训练人在真的来不及时也忽略。
const (
	WarnBefore  = 30 * 24 * time.Hour
	AlertBefore = 14 * 24 * time.Hour

	// alertEvery 是同一张证书两次到期告警之间的最小间隔。
	// 扫描每天跑一次，这个值只在主控频繁重启时起作用——而那是例行操作。
	alertEvery = 20 * time.Hour
)

// Redeployer 在证书变化后把新证书推到节点上。
//
// 证书随**每次下发**内联带上（ADR-0010），所以续期之后必须触发一次下发——
// 否则新证书会躺在主控库里，直到下一次有人改配置才下去，
// 而那期间节点上跑的还是旧的。
type Redeployer func(ctx context.Context, reason string) error

type Manager struct {
	Store    *store.Store
	Sealer   *secret.Sealer
	Hub      *ws.Hub
	Log      *slog.Logger
	Redeploy Redeployer
	// Alert 是到期告警的出口。**拆掉自动续期之后，它是唯一会主动找人的地方**
	// ——留空的话证书会安静地过期。
	Alert Alerter
}

// Alerter 与 health 那边同一个出口（alert.Notifier 实现）。
type Alerter interface {
	Notify(ctx context.Context, level, title, body string)
}

func New(m *Manager) *Manager {
	if m.Log == nil {
		m.Log = slog.Default()
	}
	return m
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

// Import 把一张外部签发的证书存进来，并**立刻触发一次下发**。
//
// 不下发的话，证书在库里、界面上显示「已导入」，而节点上还是旧的那张
// ——这正是这个仓库里反复出现的形状：**机制建好了，没接到最该接的那个输出上**。
//
// **auto_renew 强制为 false。** 主控续不了一张不是它签的证书：ACME 需要
// 那个域名的 DNS 控制权与账户绑定，而导入的证书来自别处。
//
// 更要紧的是**留着 true 的后果**：到期前 30 天，续期扫描会挑中它，
// 主控用 ACME 重签一张**覆盖掉导入的那张**——而那不会有任何提示，
// 人只会在某天发现证书的签发者变了。
func (m *Manager) Import(ctx context.Context, domain string, imp Imported) error {
	if err := m.Store.PutCert(ctx, store.Cert{
		Domain:    domain,
		Issuer:    imp.Issuer,
		Challenge: "imported", // 不是通过任何 challenge 拿到的
		AutoRenew: false,
		CertPEM:   imp.CertPEM,
		KeyPEM:    imp.KeyPEM,
		NotAfter:  imp.NotAfter,
	}, m.Sealer); err != nil {
		return fmt.Errorf("保存证书: %w", err)
	}

	m.event(ctx, "ok", fmt.Sprintf("证书 %s 已导入（签发者 %s，%s 到期）",
		domain, imp.Issuer, imp.NotAfter.Format("2006-01-02")))

	if m.Redeploy == nil {
		return nil
	}
	if err := m.Redeploy(ctx, "证书导入"); err != nil {
		// **这一步失败要说出来，而不是让「导入成功」独自站着。**
		// 证书已经在库里了，而节点上还是旧的——两句都对，合起来才是真相。
		m.Log.Error("导入后下发失败", "domain", domain, "err", err)
		m.event(ctx, "warn", fmt.Sprintf(
			"证书 %s 已导入，但下发失败，节点上仍是旧证书：%v", domain, err))
		return fmt.Errorf("证书已存下，但下发失败：%w", err)
	}
	return nil
}

// ScanExpiry 扫一遍所有证书的到期时间，该说的说出来。
//
// **拆掉自动续期之后，这是「证书到期」唯一会主动找人的地方。**
// 导入的证书没有任何东西会自动续——它会安静地走到到期那一天，
// 而那一天站点直接握不上手。
//
// 两个档位对应两种不同的动作，所以措辞和渠道都不同：
//
//	剩 30 天内  界面标黄。**不发告警**——「还早」发告警会训练人忽略这一类
//	剩 14 天内  界面标红 + 每天一条告警。这是「今天就得做」
//	已过期      crit 告警。站点此刻就是坏的
//
// 返回排查用的计数（黄、红、已过期），调用方记进日志。
func (m *Manager) ScanExpiry(ctx context.Context) (warn, urgent, expired int) {
	list, err := m.Store.ListCerts(ctx, nil)
	if err != nil {
		m.Log.Error("扫描证书到期失败", "err", err)
		return
	}
	now := time.Now()
	for _, c := range list {
		left := c.NotAfter.Sub(now)
		switch {
		case left <= 0:
			expired++
		case left < AlertBefore:
			urgent++
		case left < WarnBefore:
			warn++
			continue // 黄档只在界面上标，不惊动人
		default:
			continue
		}

		// **同一张证书一天最多报一次。**
		//
		// 扫描每天跑一次，所以这个判断平时不起作用——它挡的是主控重启：
		// 一晚上部署六次就会为同一张证书报六次警，而**一个重启就重复报警的
		// 系统会教会人忽略那一类告警**，下次真有证书要过期时他们照旧会忽略。
		// 跟 health 那边「库里已经是 down 的不再报离线」是同一条理由。
		if c.ExpiryAlertedAt != nil && now.Sub(*c.ExpiryAlertedAt) < alertEvery {
			continue
		}

		level, msg := "warn", fmt.Sprintf("证书 %s 还有 %d 天到期（%s）。"+
			"主控不会自动续期，要从证书平台重新导入一张",
			c.Domain, int(left.Hours()/24), c.NotAfter.Format("2006-01-02"))
		if left <= 0 {
			level, msg = "crit", fmt.Sprintf("证书 %s 已于 %s 过期 —— "+
				"这个域名此刻握不上 TLS。从证书平台导入一张新的",
				c.Domain, c.NotAfter.Format("2006-01-02"))
		}

		m.event(ctx, level, msg)
		if m.Alert != nil {
			m.Alert.Notify(ctx, level, "证书将到期 "+c.Domain, msg)
		}
		if err := m.Store.MarkExpiryAlerted(ctx, c.Domain); err != nil {
			// 标记不上就会重复报警 —— 说出来，别让它安静地变成噪音源。
			m.Log.Error("记录到期告警时间失败", "domain", c.Domain, "err", err)
		}
	}
	return
}

// Run 每天扫一次到期。
//
// 启动时先扫一次：主控可能停了很久，而证书不会因为没人看就不到期。
func (m *Manager) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 24 * time.Hour
	}
	t := time.NewTicker(every)
	defer t.Stop()
	if w, u, e := m.ScanExpiry(ctx); w+u+e > 0 {
		m.Log.Info("证书到期扫描", "30天内", w, "14天内", u, "已过期", e)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if w, u, e := m.ScanExpiry(ctx); w+u+e > 0 {
				m.Log.Info("证书到期扫描", "30天内", w, "14天内", u, "已过期", e)
			}
		}
	}
}
