package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/xltxb/edge_caddy/internal/dnsprovider"
	"github.com/xltxb/edge_caddy/internal/dnssched"
	"github.com/xltxb/edge_caddy/internal/model"
)

// DNS 是 api 需要的解析调度能力。
type DNS interface {
	Status(c *gin.Context, domain string) (dnssched.Status, error)
	Apply(c *gin.Context, domain string) error
}

func (h *handler) dnsReady(c *gin.Context) bool {
	if h.deps.DNS == nil || len(h.deps.Secret) == 0 {
		fail(c, http.StatusServiceUnavailable, codeInternal, "解析调度未装配")
		return false
	}
	return true
}

// getDNSProvider 返回**不含凭据**的服务商设置。
func (h *handler) getDNSProvider(c *gin.Context) {
	if len(h.deps.Secret) == 0 {
		fail(c, http.StatusServiceUnavailable, codeInternal, "解析调度未装配")
		return
	}
	cfg, err := dnsprovider.Load(c.Request.Context(), h.deps.Store, h.deps.Secret)
	if err != nil {
		h.failErr(c, err, "读取 DNS 服务商设置失败")
		return
	}
	ok(c, cfg.Public())
}

type dnsProviderInput struct {
	Kind            string `json:"kind"`
	DNSPodID        string `json:"dnspod_id"`
	DNSPodToken     string `json:"dnspod_token"`
	CloudflareToken string `json:"cloudflare_token"`
	ACMEEmail       string `json:"acme_email"`
	ACMEDirectory   string `json:"acme_directory"`
	ClearDNSPod     bool   `json:"clear_dnspod"`
	ClearCloudflare bool   `json:"clear_cloudflare"`
}

func (h *handler) putDNSProvider(c *gin.Context) {
	if len(h.deps.Secret) == 0 {
		fail(c, http.StatusServiceUnavailable, codeInternal, "解析调度未装配")
		return
	}
	var in dnsProviderInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, codeBadInput, "请求体不是合法 JSON")
		return
	}
	kind := dnsprovider.Kind(in.Kind)
	switch kind {
	case "", dnsprovider.KindCloudflare, dnsprovider.KindDNSPod:
	default:
		fail(c, http.StatusUnprocessableEntity, codeBadInput,
			"不支持的 DNS 服务商 "+in.Kind+"（目前支持 cloudflare / dnspod）")
		return
	}
	switch in.ACMEDirectory {
	case "", dnsprovider.LetsEncryptStaging, dnsprovider.LetsEncryptProduction:
	default:
		// 只允许这两个地址：任意 URL 意味着可以把签发请求指向别处，
		// 而 ACME 账号私钥会跟着发过去
		fail(c, http.StatusUnprocessableEntity, codeBadInput,
			"ACME 目录只能是 Let's Encrypt 的 staging 或正式地址")
		return
	}

	if err := dnsprovider.Merge(c.Request.Context(), h.deps.Store, h.deps.Secret, dnsprovider.Config{
		Kind: kind, DNSPodID: in.DNSPodID, DNSPodToken: in.DNSPodToken,
		CloudflareToken: in.CloudflareToken, ACMEEmail: in.ACMEEmail,
		ACMEDirectory: in.ACMEDirectory,
		ClearDNSPod:   in.ClearDNSPod, ClearCloudflare: in.ClearCloudflare,
	}); err != nil {
		h.failErr(c, err, "保存 DNS 服务商设置失败")
		return
	}
	saved, err := dnsprovider.Load(c.Request.Context(), h.deps.Store, h.deps.Secret)
	if err != nil {
		h.failErr(c, err, "读取 DNS 服务商设置失败")
		return
	}
	h.log.Info("DNS 服务商设置已更新", "operator", operatorOf(c), "kind", saved.Kind)
	// 回的是对外表示，连保存响应里也没有凭据
	ok(c, saved.Public())
}

// listWeights 返回某域名的权重表与「库里 vs 线上」的对照。
func (h *handler) getDNSSchedule(c *gin.Context) {
	if !h.dnsReady(c) {
		return
	}
	domain := c.Param("domain")
	ws, err := h.deps.Store.ListWeights(c.Request.Context(), domain)
	if err != nil {
		h.failErr(c, err, "读取解析权重失败")
		return
	}
	nodes, err := h.deps.Store.ListNodes(c.Request.Context())
	if err != nil {
		h.failErr(c, err, "读取节点失败")
		return
	}

	body := gin.H{"domain": domain, "weights": ws, "nodes": nodes, "lines": dnsprovider.AllLines}
	st, err := h.deps.DNS.Status(c, domain)
	if err != nil {
		// 读不到线上时**说出来**，而不是退化成「没有漂移」：
		// 报「一致」会让人以为解析是对的，而我们根本没看到线上是什么样
		body["live_error"] = err.Error()
	} else {
		body["planned"] = st.Planned
		body["live"] = st.Live
		body["drift"] = st.Drift
		body["drifted"] = st.Drift.Drifted()
		body["drift_summary"] = st.Drift.Summary()
	}
	ok(c, body)
}

type weightsInput struct {
	Weights []model.DNSWeight `json:"weights"`
	// Apply 为真时保存后立刻下发。
	Apply bool `json:"apply"`
}

func (h *handler) putDNSSchedule(c *gin.Context) {
	if !h.dnsReady(c) {
		return
	}
	domain := c.Param("domain")
	var in weightsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, codeBadInput, "请求体不是合法 JSON")
		return
	}
	for _, w := range in.Weights {
		if w.Weight < 0 {
			fail(c, http.StatusUnprocessableEntity, codeBadInput,
				"权重不能为负（"+w.NodeID+"）")
			return
		}
	}
	if err := h.deps.Store.PutWeights(c.Request.Context(), domain, in.Weights); err != nil {
		h.failErr(c, err, "保存解析权重失败")
		return
	}
	h.log.Info("解析权重已保存", "operator", operatorOf(c), "domain", domain, "rows", len(in.Weights))

	if !in.Apply {
		ok(c, gin.H{"domain": domain, "saved": true, "applied": false})
		return
	}
	if err := h.deps.DNS.Apply(c, domain); err != nil {
		// **不假装保存成功**：假装成功是这类界面最糟的失败方式——
		// 人以为改好了就走了，而线上一点没变
		h.log.Error("下发解析失败", "operator", operatorOf(c), "domain", domain, "err", err)
		status := http.StatusBadGateway
		if errors.Is(err, dnssched.ErrNoProvider) {
			status = http.StatusUnprocessableEntity
		}
		fail(c, status, codeInternal, "权重已保存，但下发到 DNS 服务商失败："+err.Error())
		return
	}
	ok(c, gin.H{"domain": domain, "saved": true, "applied": true})
}
