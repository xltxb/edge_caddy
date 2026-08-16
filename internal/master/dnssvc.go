package master

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/xltxb/edge_caddy/internal/dnsprovider"
	"github.com/xltxb/edge_caddy/internal/dnssched"
	"github.com/xltxb/edge_caddy/internal/store"
)

// dnsService 按当前凭据造调度器。
//
// 每次用时重建，不是启动时建好：DNS 凭据是运维在面板上改的，缓存住的话，
// 改完凭据得重启主控才生效——而那时人只会以为「改了没用」。
type dnsService struct {
	st     *store.Store
	master []byte
	log    *slog.Logger
}

func newDNSService(st *store.Store, master []byte, log *slog.Logger) *dnsService {
	return &dnsService{st: st, master: master, log: log}
}

func (s *dnsService) orch(c *gin.Context) (*dnssched.Orchestrator, error) {
	cfg, err := dnsprovider.Load(c.Request.Context(), s.st, s.master)
	if err != nil {
		return nil, err
	}
	p, err := dnsprovider.New(cfg)
	if err != nil {
		return nil, err
	}
	return dnssched.New(dnssched.Deps{Store: s.st, Provider: p, Logger: s.log}), nil
}

func (s *dnsService) Status(c *gin.Context, domain string) (dnssched.Status, error) {
	o, err := s.orch(c)
	if err != nil {
		return dnssched.Status{}, err
	}
	return o.Status(c.Request.Context(), domain)
}

func (s *dnsService) Apply(c *gin.Context, domain string) error {
	o, err := s.orch(c)
	if err != nil {
		return err
	}
	return o.Apply(c.Request.Context(), domain)
}
