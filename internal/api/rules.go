package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/store"
)

type ruleInput struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Enabled *bool          `json:"enabled"`
	Spec    model.RuleSpec `json:"spec"`
	ApplyTo []string       `json:"apply_to"`
}

func (in *ruleInput) validate() error {
	switch model.RuleType(in.Type) {
	case model.RuleIPWhitelist:
		if len(in.Spec.IPs) == 0 {
			return errors.New("白名单规则至少要有一条 IP 或 CIDR")
		}
		for _, ip := range in.Spec.IPs {
			if !validIPOrCIDR(strings.TrimSpace(ip)) {
				return errors.New(ip + " 不是合法的 IP 或 CIDR")
			}
		}
	case model.RuleServiceSecret:
		if strings.TrimSpace(in.Spec.Header) == "" {
			return errors.New("服务密钥规则必须指定请求头名")
		}
		// 没有密钥的规则会放行一切——那是一条看着生效、实际完全敞开的规则
		if strings.TrimSpace(in.Spec.Secret) == "" {
			return errors.New("服务密钥规则必须指定密钥，否则等于不设防")
		}
	case model.RuleJWTBearer:
		if strings.TrimSpace(in.Spec.JWKS) == "" {
			return errors.New("JWT 规则必须指定 JWKS 地址，否则无法验签")
		}
	default:
		return errors.New("未知的规则类型，只支持 ip_whitelist / service_secret / jwt_bearer")
	}
	return nil
}

// ruleDTO 是对外的规则表示。
//
// **不含密钥**：返回它等于把凭据发给每一个能读列表的人，而列表页会被旁人看到、
// 会进截图。只回一个 secret_set 让人能分清「没配」与「配了没显示」。
func ruleDTO(r model.AccessRule) gin.H {
	spec := gin.H{
		"ips": r.Spec.IPs, "header": r.Spec.Header, "algo": r.Spec.Algo,
		"ttl": r.Spec.TTLSec, "replay": r.Spec.Replay,
		"issuer": r.Spec.Issuer, "audience": r.Spec.Audience,
		"jwks": r.Spec.JWKS, "skew": r.Spec.SkewSec,
		"secret_set": r.Spec.Secret != "",
	}
	return gin.H{
		"id": r.ID, "name": r.Name, "type": r.Type, "enabled": r.Enabled,
		"spec": spec, "apply_to": r.ApplyTo, "version": r.Version,
		// 未绑定域名的规则不生效。那是半成品状态，不是「对所有域名生效」——
		// 后者是个危险得多的默认。
		"effective": r.Enabled && len(r.ApplyTo) > 0,
	}
}

func (h *handler) listRules(c *gin.Context) {
	rs, err := h.deps.Store.ListRules(c.Request.Context())
	if err != nil {
		h.failErr(c, err, "读取访问规则失败")
		return
	}
	out := make([]gin.H, 0, len(rs))
	for _, r := range rs {
		out = append(out, ruleDTO(r))
	}
	ok(c, gin.H{"rules": out})
}

func (h *handler) putRule(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		fail(c, http.StatusBadRequest, codeBadInput, "缺少规则 ID")
		return
	}
	var in ruleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, codeBadInput, "请求体不是合法 JSON")
		return
	}
	if err := in.validate(); err != nil {
		fail(c, http.StatusBadRequest, codeBadInput, err.Error())
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ctx := c.Request.Context()

	// 密钥留空表示「不改」，而不是「清空」——否则每次改别的字段都会把密钥抹掉
	if in.Spec.Secret == "" {
		if existing, err := h.findRule(ctx, id); err == nil {
			in.Spec.Secret = existing.Spec.Secret
		}
	}
	rule := model.AccessRule{
		ID: id, Name: in.Name, Type: model.RuleType(in.Type),
		Enabled: enabled, Spec: in.Spec, ApplyTo: nonNilStrings(in.ApplyTo),
	}
	if err := h.deps.Store.PutRule(ctx, rule); err != nil {
		h.failErr(c, err, "写入访问规则失败")
		return
	}
	h.log.Info("写入访问规则", "operator", operatorOf(c), "id", id, "type", in.Type)
	ok(c, ruleDTO(rule))
}

func (h *handler) findRule(ctx context.Context, id string) (model.AccessRule, error) {
	rs, err := h.deps.Store.ListRules(ctx)
	if err != nil {
		return model.AccessRule{}, err
	}
	for _, r := range rs {
		if r.ID == id {
			return r, nil
		}
	}
	return model.AccessRule{}, store.ErrNotFound
}

func (h *handler) deleteRule(c *gin.Context) {
	err := h.deps.Store.DeleteRule(c.Request.Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, codeNotFound, "规则不存在")
		return
	}
	if err != nil {
		h.failErr(c, err, "删除访问规则失败")
		return
	}
	h.log.Info("删除访问规则", "operator", operatorOf(c), "id", c.Param("id"))
	ok(c, nil)
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
