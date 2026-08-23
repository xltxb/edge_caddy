package api

import (
	"github.com/xltxb/edge_caddy/internal/certs"
	"time"

	"github.com/gin-gonic/gin"
)

type certResp struct {
	Domain string `json:"domain"`
	Issuer string `json:"issuer"`
	// Challenge 是「这张证书怎么来的」：imported（外部平台推进来的）
	// 或 dns-01（ADR-0015 之前主控自己签的，历史数据）。
	//
	// **名字不好**：这个词在 ACME 里指校验方式，而 imported 恰恰是
	// 「没有经过任何校验」。留着它是因为库里那一列就叫这个名字，
	// 而现在改名要动迁移、契约、前端三处 —— 记在这里，别让它悄悄成为惯例。
	Challenge string `json:"challenge"`
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
			NotAfter: cert.NotAfter.Format(time.RFC3339),
			DaysLeft: int(time.Until(cert.NotAfter).Hours() / 24),
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

	// **先绑定，再看装配。** 「你发了一个契约里没有的字段」不需要查任何状态就能知道，
	// 而先报「证书管理未装配」会把人支到部署配置上去查一个根本不在那儿的问题。
	var req importCertReq
	if err := bindStrict(c, &req); err != nil {
		Fail(c, CodeBadParam, err.Error())
		return
	}
	if s.certs == nil {
		Fail(c, CodeStateConflict, "证书管理未装配")
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
		"domain":    domain,
		"issuer":    imp.Issuer,
		"not_after": imp.NotAfter.Format(time.RFC3339),
		"days_left": int(time.Until(imp.NotAfter).Hours() / 24),
		"domains":   imp.Domains,
		"warnings":  imp.Warnings,
		"detail":    "已导入并下发到各节点",
	})
}
