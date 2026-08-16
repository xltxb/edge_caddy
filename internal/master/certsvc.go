package master

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xltxb/edge_caddy/internal/certs"
	"github.com/xltxb/edge_caddy/internal/dnsprovider"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

// certService 把证书的各块拼成 api.Certs。
//
// 签发器**每次用时重建**，不是启动时建好：DNS 凭据是运维在面板上改的，
// 缓存住的话，改完凭据得重启主控才生效——而那时人只会以为「改了没用」。
type certService struct {
	st     *store.Store
	master []byte
	inv    *certs.Inventory
	log    *slog.Logger
	alerts certs.Alerts

	mu  sync.Mutex
	mgr *certs.Manager
}

func newCertService(st *store.Store, master []byte, inv *certs.Inventory,
	alerts certs.Alerts, log *slog.Logger) *certService {
	return &certService{st: st, master: master, inv: inv, alerts: alerts, log: log}
}

func (s *certService) Inventory() []certs.Aggregated { return s.inv.Aggregate() }

func (s *certService) Domains(c *gin.Context) ([]string, error) {
	return certs.NewSource(s.st, s.master).Domains(c.Request.Context())
}

// Issue 为一个域名签发/续期，同步返回结果。
func (s *certService) Issue(c *gin.Context, domain string) error {
	ctx := c.Request.Context()
	mgr, err := s.manager(ctx)
	if err != nil {
		return err
	}
	// 走 Manager 而不是直接调签发器：续期窗口、退避、失败保留旧证书这几条
	// 都在它那里，绕过去等于把那几条保护也绕过去
	return mgr.RenewNow(ctx, domain)
}

// manager 按当前凭据造一个管理器。
func (s *certService) manager(ctx context.Context) (*certs.Manager, error) {
	cfg, err := dnsprovider.Load(ctx, s.st, s.master)
	if err != nil {
		return nil, err
	}
	p, err := dnsprovider.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("DNS 服务商未配置好：%w", err)
	}
	iss, err := certs.NewACMEIssuer(certs.ACMEConfig{
		DNS: p, Directory: cfg.ACMEDirectory, Email: cfg.ACMEEmail,
		Store: s.st, Master: s.master, Logger: s.log,
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mgr = certs.NewManager(certs.ManagerDeps{
		Store: s.st, Master: s.master, Issuer: iss, Alerts: s.alerts, Logger: s.log,
	})
	return s.mgr, nil
}

// alerts 由装配时注入。
var _ certs.Alerts = (*certAlerts)(nil)

type certAlerts struct {
	emit func(kind, domain, msg string)
}

func (a *certAlerts) CertAlert(kind, domain, msg string) {
	if a.emit != nil {
		a.emit(kind, domain, msg)
	}
}

// RunRenewal 周期性巡检续期。
func (s *certService) RunRenewal(ctx context.Context, interval time.Duration) {
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
			mgr, err := s.manager(ctx)
			if err != nil {
				// 没配 DNS 服务商时不算错误：这个系统可以只做反代不做 TLS
				s.log.Debug("跳过续期巡检", "reason", err)
				continue
			}
			domains, err := certs.NewSource(s.st, s.master).Domains(ctx)
			if err != nil {
				s.log.Error("读取待续期域名失败", "err", err)
				continue
			}
			_ = mgr.Ensure(ctx, domains)
		}
	}
}

// certCollector 把隧道上报的证书清单喂给内存视图。
type certCollector struct{ inv *certs.Inventory }

func (c *certCollector) OnNodeCerts(nodeID string, loaded []tunnel.LoadedCert) {
	items := make([]certs.NodeCert, 0, len(loaded))
	for _, l := range loaded {
		notAfter, err := time.Parse(time.RFC3339, l.NotAfter)
		if err != nil {
			// 解不出时间的条目跳过，而不是记成零值：零值会被算成
			// 「1 年前就过期了」，把整个域名的聚合结果拉成红色
			continue
		}
		items = append(items, certs.NodeCert{
			Domain: l.Domain, NotAfter: notAfter,
			Issuer: l.Issuer, KeyType: l.KeyType, Serial: l.Serial,
		})
	}
	c.inv.Report(certs.NodeReport{NodeID: nodeID, Certs: items})
}

// CertSweepInterval 是证书清单的刷新周期。
//
// 取 5 分钟，与 certs.StaleAfter（10 分钟）留一倍余量：正好错过一次巡检
// 不该立刻被标成「数据陈旧」，那会让人去查一台其实没问题的机器。
const CertSweepInterval = 5 * time.Minute

// Prober 是主控向节点发起探活的能力。
type Prober interface {
	Connected() []string
	Probe(ctx context.Context, nodeID string, timeout time.Duration) (tunnel.ProbeReport, error)
}

// runCertSweep 周期性探活所有在线节点，刷新证书清单。
//
// 证书视图是内存里的，只在「有人点了探活」时更新的话，面板打开的第一眼
// 永远是空的；而离线节点的数据会自然变旧，界面上会标出来。
func runCertSweep(ctx context.Context, p Prober, log *slog.Logger, interval time.Duration) {
	if interval <= 0 {
		interval = CertSweepInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, id := range p.Connected() {
				// 探活失败不处理：那台节点的数据会自然变旧，界面上会标出来。
				// 在这里补一条告警只会与健康巡检重复。
				if _, err := p.Probe(ctx, id, 5*time.Second); err != nil {
					log.Debug("证书清单刷新失败", "node_id", id, "err", err)
				}
			}
		}
	}
}
