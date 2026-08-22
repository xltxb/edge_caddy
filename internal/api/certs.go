package api

import (
	"errors"
	"github.com/xltxb/edge_caddy/internal/certs"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/store"
)

type certResp struct {
	Domain    string `json:"domain"`
	Issuer    string `json:"issuer"`
	Challenge string `json:"challenge"`
	AutoRenew bool   `json:"auto_renew"`
	NotAfter  string `json:"not_after"`
	DaysLeft  int    `json:"days_left"`

	// **两列真相。**
	//
	// ExpectedNodes 是主控账面：主控签发了它，应当覆盖这么多节点。
	// LoadedNodes / MissingNodes 是节点回执：那台机器上 TLS 实际出示的证书。
	//
	// loaded < expected 意味着**下发到了但没生效**。这类故障在「节点自管证书」
	// 的模型里根本看不见，是这套设计换来的主要能力。
	ExpectedNodes int      `json:"expected_nodes"`
	LoadedNodes   int      `json:"loaded_nodes"`
	MissingNodes  []string `json:"missing_nodes"`
}

func (s *Server) handleListCerts(c *gin.Context) {
	ctx := c.Request.Context()

	// sealer 传 nil：这个端点不需要私钥，而私钥不该在不必要的地方出现。
	certs, err := s.store.ListCerts(ctx, nil)
	if err != nil {
		s.log.Error("读取证书失败", "err", err)
		Fail(c, CodeDownstream, "读取证书失败")
		return
	}
	receipts, err := s.store.ListCertReceipts(ctx)
	if err != nil {
		s.log.Error("读取证书回执失败", "err", err)
		Fail(c, CodeDownstream, "读取证书失败")
		return
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.log.Error("读取节点失败", "err", err)
		Fail(c, CodeDownstream, "读取证书失败")
		return
	}

	loadedBy := map[string]map[string]bool{}
	for _, r := range receipts {
		if loadedBy[r.Domain] == nil {
			loadedBy[r.Domain] = map[string]bool{}
		}
		loadedBy[r.Domain][r.NodeID] = true
	}

	items := make([]certResp, 0, len(certs))
	for _, cert := range certs {
		item := certResp{
			Domain: cert.Domain, Issuer: cert.Issuer, Challenge: cert.Challenge,
			AutoRenew: cert.AutoRenew,
			NotAfter:  cert.NotAfter.Format(time.RFC3339),
			DaysLeft:  int(time.Until(cert.NotAfter).Hours() / 24),
			// 期望覆盖的是**全部节点**：证书随每次下发内联带给每一台
			// （ADR-0010），不存在「只给某几台」这回事。
			ExpectedNodes: len(nodes),
			MissingNodes:  []string{},
		}
		for _, n := range nodes {
			if loadedBy[cert.Domain][n.ID] {
				item.LoadedNodes++
			} else {
				item.MissingNodes = append(item.MissingNodes, n.ID)
			}
		}
		items = append(items, item)
	}
	OK(c, gin.H{"items": items})
}

func (s *Server) handleRenewCert(c *gin.Context) {
	domain := c.Param("domain")
	setAuditTarget(c, domain)

	if s.certs == nil {
		Fail(c, CodeStateConflict, "证书签发未装配")
		return
	}
	// 对**有路由但还没有证书**的域名也放行：那是首次签发。
	// 只认已有证书的话，第一张证书就永远签不出来——而人配了一个域名，
	// 本来就该有证书，不该还要先手动做点别的。
	ctx := c.Request.Context()
	_, certErr := s.store.GetCert(ctx, domain, nil)
	_, routeErr := s.store.GetRoute(ctx, domain)
	if errors.Is(certErr, store.ErrNotFound) && errors.Is(routeErr, store.ErrNotFound) {
		Fail(c, CodeNotFound, "没有这个域名的证书，也没有对应的路由")
		return
	}

	// 异步：ACME 签发要跟服务商往返，同步等会把 HTTP 请求拖很久。
	// 结果经 WS 的 event 帧回报（契约 §9）。
	s.certs.RenewAsync(domain)
	OK(c, gin.H{"domain": domain, "accepted": true})
}

func (s *Server) handleRenewCheck(c *gin.Context) {
	if s.certs == nil {
		Fail(c, CodeStateConflict, "证书签发未装配")
		return
	}
	n := s.certs.RenewDueAsync()
	OK(c, gin.H{"accepted": true, "queued": n})
}

type importCertReq struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

// handleImportCert 从外部证书平台导入一张证书（PUT /certs/:domain）。
//
// 这是主控获取证书的**第二条路**，与 ACME 并行。节点那一侧完全不变：
// 仍然是主控集中持有、经隧道内联下发（ADR-0001、ADR-0010），
// 节点照旧不持有 DNS 凭据、不自行申请。
//
// **校验在存之前做完。** 一张不匹配的证书会一路走到节点上：存库成功、
// 下发成功、界面显示「已导入」——而站点是坏的。
func (s *Server) handleImportCert(c *gin.Context) {
	domain := c.Param("domain")
	setAuditTarget(c, domain)

	if s.certs == nil {
		Fail(c, CodeStateConflict, "证书管理未装配")
		return
	}
	var req importCertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, CodeBadParam, "请求格式错误")
		return
	}
	if req.CertPEM == "" || req.KeyPEM == "" {
		FailValidation(c, "证书与私钥都要给", []FieldError{
			{ResKey: "cert:" + domain, Field: "cert_pem", Reason: "必填"},
			{ResKey: "cert:" + domain, Field: "key_pem", Reason: "必填"},
		})
		return
	}

	imp, err := certs.ValidateImport(domain, []byte(req.CertPEM), []byte(req.KeyPEM))
	if err != nil {
		// **校验失败是 1002 而不是 1001。**
		//
		// 1001 是「格式错」，而这里的失败多半是内容不对：证书配不上私钥、
		// 覆盖的域名不是这个、已经过期。人拿到 1001 会去检查 JSON 有没有写错，
		// 而问题在 PEM 里面。
		FailValidation(c, err.Error(), []FieldError{
			{ResKey: "cert:" + domain, Field: "cert_pem", Reason: err.Error()},
		})
		return
	}

	if err := s.certs.Import(c.Request.Context(), domain, imp); err != nil {
		s.log.Error("导入证书失败", "domain", domain, "err", err)
		// 证书可能已经存下了而下发失败——那时 detail 会说清，
		// 而审计要记成 partial 而不是 fail。
		setAuditPartial(c, err.Error())
		Fail(c, CodeDownstream, err.Error())
		return
	}

	// **私钥不回显**（PRD §7，与规则的共享密钥、DNS 凭证同一条）。
	// 回的是「我们从这张证书里读出了什么」——那让人当场看得出
	// 自己传的是不是想传的那张。
	OK(c, gin.H{
		"domain":     domain,
		"issuer":     imp.Issuer,
		"not_after":  imp.NotAfter.Format(time.RFC3339),
		"days_left":  int(time.Until(imp.NotAfter).Hours() / 24),
		"domains":    imp.Domains,
		"auto_renew": false,
		"warnings":   imp.Warnings,
		"detail":     "已导入并下发到各节点",
	})
}
