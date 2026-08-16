package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/xltxb/edge_caddy/internal/certs"
)

// Certs 是 api 需要的证书能力。
type Certs interface {
	// Inventory 是节点实时上报的聚合视图（不落库）。
	Inventory() []certs.Aggregated
	// Issue 为一个域名签发/续期证书，同步返回结果。
	Issue(c *gin.Context, domain string) error
	// Domains 是当前已有证书的域名。
	Domains(c *gin.Context) ([]string, error)
}

// listCerts 返回证书聚合视图。
//
// 状态**不落库**（PRD §4），来源是各节点实时上报：落库只会得到一份随时可能
// 过时的副本，而「过时的证书状态」比没有更危险——它会让人以为一张已经换掉的
// 证书还在生效。
func (h *handler) listCerts(c *gin.Context) {
	if h.deps.Certs == nil {
		fail(c, http.StatusServiceUnavailable, codeInternal, "证书功能未装配")
		return
	}
	inv := h.deps.Certs.Inventory()
	ok(c, gin.H{
		"certs": inv,
		// 分档由后端给，前端不自己算：前端自己算的话，「什么算紧急」
		// 就有了两个定义，改一处忘一处
		"bands":           gin.H{"crit_below_days": certs.CritBelowDays, "warn_below_days": certs.WarnBelowDays},
		"stale_after_sec": int64(certs.StaleAfter.Seconds()),
	})
}

type renewInput struct {
	Domain string `json:"domain"`
}

// renewCert 为单个域名续期。同步返回结果——人点了按钮就是在等这个答案。
func (h *handler) renewCert(c *gin.Context) {
	if h.deps.Certs == nil {
		fail(c, http.StatusServiceUnavailable, codeInternal, "证书功能未装配")
		return
	}
	domain := c.Param("domain")
	if domain == "" {
		fail(c, http.StatusBadRequest, codeBadInput, "缺少域名")
		return
	}
	if err := h.deps.Certs.Issue(c, domain); err != nil {
		// 原因原样带给用户：「DNS 凭据无效」和「域名不在这个账号下」
		// 是两件事，处理方式完全不同
		h.log.Warn("续期失败", "operator", operatorOf(c), "domain", domain, "err", err)
		fail(c, http.StatusBadGateway, codeInternal, "续期失败："+err.Error())
		return
	}
	h.log.Info("证书已续期", "operator", operatorOf(c), "domain", domain)
	ok(c, gin.H{"domain": domain, "renewed": true})
}

// renewAllCerts 逐个域名续期检查，**逐项反馈**。
//
// 不是「全部成功/全部失败」：一个域名的 DNS 配错了不该让其它域名的结果
// 也看不见，而那正是运维需要知道的——哪几个好了、哪几个没好。
func (h *handler) renewAllCerts(c *gin.Context) {
	if h.deps.Certs == nil {
		fail(c, http.StatusServiceUnavailable, codeInternal, "证书功能未装配")
		return
	}
	domains, err := h.deps.Certs.Domains(c)
	if err != nil {
		h.failErr(c, err, "读取证书列表失败")
		return
	}
	type row struct {
		Domain string `json:"domain"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	rows := make([]row, 0, len(domains))
	for _, d := range domains {
		if err := h.deps.Certs.Issue(c, d); err != nil {
			rows = append(rows, row{Domain: d, OK: false, Detail: err.Error()})
			continue
		}
		rows = append(rows, row{Domain: d, OK: true, Detail: "已检查"})
	}
	h.log.Info("全部续期检查完成", "operator", operatorOf(c), "domains", len(rows))
	ok(c, gin.H{"results": rows})
}
