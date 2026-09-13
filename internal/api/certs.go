package api

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xltxb/edge_caddy/internal/certs"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/store"
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
	// Domains 是这张证书**实际覆盖**的域名（含通配符）。
	//
	// 一张 *.example.com 的证书在列表里看不出它覆盖什么，
	// 而那正是人想确认的第一件事。**从证书本身读，不落库**：
	// 存一份副本意味着两处真相，而两处迟早会分叉。
	Domains []string `json:"domains"`

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

	// Covers 是这张证书**正在服务**的路由域名。
	//
	// 与 Domains 的区别是承重的：Domains 是「它能服务什么」（证书自己说的），
	// Covers 是「它实际服务着什么」（跟我们的路由对过之后）。
	// **空数组 = 存着但没人用**：它仍然会被下发到每台节点、仍然会报到期，
	// 而没有任何站点用它。删掉它是安全的，而这一列是唯一说得出这件事的地方。
	//
	// **读不到路由清单时是 nil（JSON `null`），不是空数组**（§0.4）。
	// `[]` 是「它什么都没覆盖」——那是个很强的断言，会引着人去删；
	// 而「我算不出来」不该长成那个样子。跟 reconnects_1h 是同一条理由。
	Covers []string `json:"covers"`
}

func (s *Server) handleListCerts(c *gin.Context) {
	ctx := c.Request.Context()

	// sealer 传 nil：这个端点不需要私钥，而私钥不该在不必要的地方出现。
	// **不叫 certs**：那会遮住同名的包，而这个函数要用 certs.DomainsOf。
	// 遮蔽不报错，它只是让那个包在这个作用域里消失——
	// 我为此写了一行编译不过的代码，然后花了一轮才看出原因。
	list, err := s.store.ListCerts(ctx, nil)
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

	// **读不到路由清单不是致命的**：证书列表的主体（到期、下发情况）
	// 跟路由无关，为了一列附加信息把整页变成一个错误页是不成比例的。
	// 但那一列必须是 null 而不是空数组——见 certResp.Covers。
	routes, routeErr := s.store.ListRoutes(ctx)
	if routeErr != nil {
		s.log.Error("读取路由清单失败，证书列表的 covers 这一列给不出", "err", routeErr)
	}
	served := routeDomains(routes)

	loadedBy := map[string]map[string]bool{}
	for _, r := range receipts {
		if loadedBy[r.Domain] == nil {
			loadedBy[r.Domain] = map[string]bool{}
		}
		loadedBy[r.Domain][r.NodeID] = true
	}

	items := make([]certResp, 0, len(list))
	for _, cert := range list {
		item := certResp{
			Domain: cert.Domain, Issuer: cert.Issuer, Challenge: cert.Challenge,
			// 从证书本身读，不落库：存一份副本意味着两处真相，而两处迟早分叉。
			Domains:  certs.DomainsOf(cert.CertPEM),
			NotAfter: cert.NotAfter.Format(time.RFC3339),
			DaysLeft: int(time.Until(cert.NotAfter).Hours() / 24),
			// 期望覆盖的是**全部节点**：证书随每次下发内联带给每一台
			// （ADR-0010），不存在「只给某几台」这回事。
			ExpectedNodes: len(nodes),
			MissingNodes:  []string{},
		}
		if routeErr == nil {
			// 一定要是非 nil 的切片：CoveredBy 一个都没匹配到时回 nil，
			// 而**「算过了，一个都没覆盖」和「没算」在 JSON 里必须长得不一样**。
			item.Covers = []string{}
			item.Covers = append(item.Covers, certs.CoveredBy(cert.CertPEM, served)...)
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
// 仍然是主控集中持有、经隧道内联下发（ADR-0010），节点照旧不持有 DNS
// 凭据、不自行申请 —— ADR-0015 推翻的是「主控签发」，不是这一条。
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

	// **这张证书管得着我们服务的域名吗？管不着就拒。**
	//
	// 起因是三方证书平台的对接：它们那套语义要区分「这个域名不归这个 CDN
	// 管，跳过不算失败」和「真的部署失败，要告警」。收下的话它们只看得到
	// 200，两种情况混成一种。
	//
	// 而收下的代价不只是它们那边：这张证书会进到期扫描，**每天为一个
	// 我们根本不服务的域名报警**——而到期告警是拆掉自动续期之后唯一
	// 会主动找人的东西，往里掺噪音等于在削弱它。
	//
	// 判据用 CoversAny（VerifyHostname），**不是「有没有一条路由等于它」**：
	// 证书不按路由挑，主控把全部证书内联进每个节点、Caddy 按 SNI 配对，
	// 所以一张 *.example.com 服务着 a.example.com，哪怕没有路由叫那个名字。
	//
	// 守着这一条的是 TestWildcardCertIsAcceptedForTheHostsItCovers
	// （internal/e2e）：把判据换成「名字相等」，它当场红。
	// 没有那条的话，一个 route.Domain == domain 的实现也能让「拒绝陌生域名」
	// 那条全绿，而它会拒掉通配符证书——**而通配符正是三方平台最常发的那一类**。
	routes, err := s.store.ListRoutes(c.Request.Context())
	if err != nil {
		// **查不出来不能当成「不归我们管」。** 那会把一次数据库抖动
		// 变成一句「这个域名不在这个 CDN 上」，而对方的语义是「跳过，不告警」
		// —— 于是一次真的故障被静音了。
		s.log.Error("读取路由清单失败", "domain", domain, "err", err)
		Fail(c, CodeDownstream, "读不到路由清单，无法判断这个域名归不归本 CDN 管，"+
			"这次导入没有执行")
		return
	}
	served := make([]string, 0, len(routes))
	for _, r := range routes {
		served = append(served, r.Domain)
	}
	if !imp.CoversAny(served) {
		Fail(c, CodeNotFound, fmt.Sprintf(
			"这张证书覆盖的域名（%s）没有一个是本 CDN 在服务的。"+
				"先在「路由」里建好站点再推证书 —— 顺序反过来的话，"+
				"证书会存在这里等着一个永远不来的站点",
			strings.Join(imp.Domains, ", ")))
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

// handleDeleteCert 删掉一张证书。
//
// **默认拒绝删一张还在服务的证书。** 删了它，那些域名下一次下发之后
// 就握不上 TLS —— 而这跟「证书过期」不同：过期还有几天窗口，
// 这个是**按下按钮的那一刻站点就坏了**。
//
// 但不能做成硬约束。一张 *.example.com 可能覆盖着二十条路由，
// 要求「先把覆盖到的路由都删掉」等于要求不可能的事，而人会绕开——
// 直接进数据库删。**一道逼人绕开的门比没有门更糟**：绕过去之后
// 下发不会被触发，节点上那张证书会一直留着。
//
// 所以给逃生口：`?force=true`。拒绝那句里写清它，人才知道有这条路。
func (s *Server) handleDeleteCert(c *gin.Context) {
	domain := c.Param("domain")
	setAuditTarget(c, domain)

	if s.certs == nil {
		Fail(c, CodeStateConflict, "证书管理未装配")
		return
	}

	if c.Query("force") != "true" {
		covers, err := s.coveredRoutes(c, domain)
		if err != nil {
			return // coveredRoutes 已经回过错了
		}
		if len(covers) > 0 {
			Fail(c, CodeStateConflict, fmt.Sprintf(
				"这张证书正在服务 %s。删掉之后这些域名下一次下发就握不上 TLS —— "+
					"确定要删就带上 force=true",
				strings.Join(covers, "、")))
			return
		}
	}

	if err := s.certs.Delete(c.Request.Context(), domain); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			Fail(c, CodeNotFound, "没有这张证书")
			return
		}
		s.log.Error("删除证书失败", "domain", domain, "err", err)
		// 库里删了而下发失败时，审计记 partial：那不是「没删成」。
		setAuditPartial(c, err.Error())
		Fail(c, CodeDownstream, err.Error())
		return
	}
	OK(c, gin.H{"domain": domain, "detail": "已删除并从各节点上摘掉"})
}

// coveredRoutes 回这张证书正在服务哪些路由。出错时它自己回错并回 nil, err。
//
// **判据是 leaf.VerifyHostname，与导入时那道 1003 用的是同一个。**
// 分开写的话，「收得进来」和「删得掉」会对不上账。
func (s *Server) coveredRoutes(c *gin.Context, domain string) ([]string, error) {
	ctx := c.Request.Context()
	cert, err := s.store.GetCert(ctx, domain, nil)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			Fail(c, CodeNotFound, "没有这张证书")
			return nil, err
		}
		s.log.Error("读取证书失败", "domain", domain, "err", err)
		Fail(c, CodeDownstream, "读取证书失败")
		return nil, err
	}
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		// **查不出来不能当成「它谁也没服务」。** 那会让一次数据库抖动
		// 变成一次静默的、把站点弄坏的删除。
		s.log.Error("读取路由清单失败", "err", err)
		Fail(c, CodeDownstream, "读不到路由清单，判断不出这张证书还在不在服务，"+
			"这次删除没有执行")
		return nil, err
	}
	return certs.CoveredBy(cert.CertPEM, routeDomains(routes)), nil
}

func routeDomains(routes []model.Route) []string {
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r.Domain)
	}
	return out
}
