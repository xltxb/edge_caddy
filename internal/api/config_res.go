package api

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
	"github.com/xltxb/edge_caddy/internal/store"
)

func (s *Server) handleListRoutes(c *gin.Context) {
	routes, err := s.store.ListRoutes(c.Request.Context())
	if err != nil {
		s.log.Error("读取路由失败", "err", err)
		Fail(c, CodeDownstream, "读取路由失败")
		return
	}
	if routes == nil {
		routes = []model.Route{}
	}
	OK(c, gin.H{"items": routes})
}

func (s *Server) handleCreateRoute(c *gin.Context) {
	var r model.Route
	if err := c.ShouldBindJSON(&r); err != nil {
		Fail(c, CodeBadParam, "请求格式错误")
		return
	}
	setAuditTarget(c, r.Domain)

	if issues := render.Validate([]model.Route{r}, nil); len(issues) > 0 {
		FailValidation(c, "新建路由未通过校验", toFieldErrors(issues))
		return
	}

	ctx := c.Request.Context()
	if _, err := s.store.GetRoute(ctx, r.Domain); err == nil {
		Fail(c, CodeConflict, "该域名已存在")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.log.Error("查重失败", "err", err)
		Fail(c, CodeDownstream, "新建路由失败")
		return
	}

	if err := s.store.CreateRoute(ctx, r); err != nil {
		s.log.Error("新建路由失败", "err", err)
		Fail(c, CodeDownstream, "新建路由失败")
		return
	}
	OK(c, gin.H{"domain": r.Domain})
}

func (s *Server) handleListDrafts(c *gin.Context) {
	drafts, err := s.store.ListDrafts(c.Request.Context())
	if err != nil {
		s.log.Error("读取草稿失败", "err", err)
		Fail(c, CodeDownstream, "读取草稿失败")
		return
	}
	items := map[string]json.RawMessage{}
	updated := map[string]gin.H{}
	for _, d := range drafts {
		items[d.ResKey] = d.Patch
		updated[d.ResKey] = gin.H{"by": d.UpdatedBy, "at": d.UpdatedAt}
	}
	OK(c, gin.H{"items": items, "updated": updated})
}

func (s *Server) handlePutDraft(c *gin.Context) {
	key := c.Param("key")
	setAuditTarget(c, key)

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		Fail(c, CodeBadParam, "读取请求体失败")
		return
	}
	if !json.Valid(body) {
		Fail(c, CodeBadParam, "草稿必须是合法 JSON 对象")
		return
	}

	p, _ := principalOf(c)
	if err := s.store.PutDraft(c.Request.Context(), key, body, p.Name); err != nil {
		s.log.Error("写草稿失败", "err", err)
		Fail(c, CodeDownstream, "写草稿失败")
		return
	}
	OK(c, gin.H{"res_key": key})
}

func (s *Server) handleDeleteDrafts(c *gin.Context) {
	if err := s.store.DeleteAllDrafts(c.Request.Context()); err != nil {
		s.log.Error("放弃草稿失败", "err", err)
		Fail(c, CodeDownstream, "放弃草稿失败")
		return
	}
	OK(c, nil)
}

func toFieldErrors(issues []render.Issue) []FieldError {
	out := make([]FieldError, 0, len(issues))
	for _, i := range issues {
		out = append(out, FieldError{ResKey: i.ResKey, Field: i.Field, Reason: i.Reason})
	}
	return out
}

func (s *Server) handleUpdateRoute(c *gin.Context) {
	domain := c.Param("domain")
	setAuditTarget(c, domain)

	var r model.Route
	if err := c.ShouldBindJSON(&r); err != nil {
		Fail(c, CodeBadParam, "请求格式错误")
		return
	}
	// 路径里的域名是权威的：改域名等于删一条建一条，不是「编辑」。
	r.Domain = domain

	ctx := c.Request.Context()
	if _, err := s.store.GetRoute(ctx, domain); errors.Is(err, store.ErrNotFound) {
		Fail(c, CodeNotFound, "没有这条路由")
		return
	}
	if issues := render.Validate([]model.Route{r}, nil); len(issues) > 0 {
		FailValidation(c, "路由未通过校验", toFieldErrors(issues))
		return
	}
	if err := s.store.UpsertRoute(ctx, r); err != nil {
		s.log.Error("修改路由失败", "err", err)
		Fail(c, CodeDownstream, "修改路由失败")
		return
	}
	OK(c, gin.H{"domain": domain})
}

// handleDeleteRoute 删除路由，并**联动**把该域名从所有访问规则的绑定里摘掉。
//
// 留着一条指向已删域名的绑定，会让人以为那个域名还受保护——而它连路由都没有了。
func (s *Server) handleDeleteRoute(c *gin.Context) {
	domain := c.Param("domain")
	setAuditTarget(c, domain)
	ctx := c.Request.Context()

	if _, err := s.store.GetRoute(ctx, domain); errors.Is(err, store.ErrNotFound) {
		Fail(c, CodeNotFound, "没有这条路由")
		return
	}
	unbound, err := s.store.UnbindDomain(ctx, domain)
	if err != nil {
		s.log.Error("摘除域名绑定失败", "err", err)
		Fail(c, CodeDownstream, "删除路由失败")
		return
	}
	if err := s.store.DeleteRoute(ctx, domain); err != nil {
		s.log.Error("删除路由失败", "err", err)
		Fail(c, CodeDownstream, "删除路由失败")
		return
	}
	// 顺手清掉它的草稿：留着一份指向已删资源的草稿，会让「有几处未下发改动」
	// 这个数字算上一个再也下发不出去的东西。
	if err := s.store.DeleteDraft(ctx, "route:"+domain); err != nil {
		s.log.Error("清理草稿失败", "err", err)
	}
	if unbound == nil {
		unbound = []string{}
	}
	OK(c, gin.H{"deleted": domain, "unbound_rules": unbound})
}

// handleDeleteRule 删掉一条访问规则。
//
// 这个端点原先没有：路由有 PUT|DELETE，规则只有 PUT。于是一条 id 打错的规则
// 会永远躺在列表里——**「停用」和「解绑域名」都是让它不生效，不是让它不在。**
//
// 是前端 agent 撞见的：他的前提检查脚本在库里留了个夹具，删不掉。而他指出的
// 那一层比缺口本身更要紧——界面上既没有删除按钮、也没说不能删，
// 人会找一圈然后以为是自己没找到。
func (s *Server) handleDeleteRule(c *gin.Context) {
	id := c.Param("id")
	setAuditTarget(c, id)
	ctx := c.Request.Context()

	// sealer 传 nil：只是确认它存在，不需要解密共享密钥。
	if _, err := s.store.GetRule(ctx, id); errors.Is(err, store.ErrNotFound) {
		Fail(c, CodeNotFound, "没有这条访问规则")
		return
	}
	if err := s.store.DeleteRule(ctx, id); err != nil {
		s.log.Error("删除访问规则失败", "rule", id, "err", err)
		Fail(c, CodeDownstream, "删除访问规则失败")
		return
	}
	// 草稿跟着走，与删路由同一条理由：留一份指向已删资源的草稿，
	// 会让「有几处未下发改动」这个数字算上一个再也下发不出去的东西。
	if err := s.store.DeleteDraft(ctx, "rule:"+id); err != nil {
		s.log.Error("清理草稿失败", "rule", id, "err", err)
	}
	OK(c, gin.H{"deleted": id})
}

func (s *Server) handleListRules(c *gin.Context) {
	// sealer 传 nil：这个端点不解共享密钥。凭证只写入不回显（PRD §7），
	// spec 里回的是 secret_configured 而不是密钥本身。
	rules, err := s.store.ListRules(c.Request.Context(), nil)
	if err != nil {
		s.log.Error("读取访问规则失败", "err", err)
		Fail(c, CodeDownstream, "读取访问规则失败")
		return
	}
	if rules == nil {
		rules = []model.Rule{}
	}
	OK(c, gin.H{"items": rules})
}

type ruleReq struct {
	model.Rule
	// Secret 空串表示保持不变——凭证不回显，前端也带不出原值。
	Secret string `json:"secret"`
}

func (s *Server) handleUpsertRule(c *gin.Context) {
	id := c.Param("id")
	setAuditTarget(c, id)

	var req ruleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, CodeBadParam, "请求格式错误")
		return
	}
	req.Rule.ID = id

	ctx := c.Request.Context()
	// 校验时把已有的密钥算上：一条已经配好密钥的规则，前端提交时带不出原值，
	// 不这么做的话每次保存都会报「尚未设置共享密钥」。
	toValidate := req.Rule
	if req.Secret != "" {
		toValidate.Secret = req.Secret
	} else if cur, err := s.store.GetRule(ctx, id); err == nil && cur.Spec.SecretConfigured {
		toValidate.Secret = "（已配置）"
	}

	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		s.log.Error("读取路由失败", "err", err)
		Fail(c, CodeDownstream, "保存失败")
		return
	}
	if issues := render.Validate(routes, []model.Rule{toValidate}); len(issues) > 0 {
		// 只回与这条规则有关的问题：路由那边的问题不该在保存规则时冒出来。
		var mine []FieldError
		for _, i := range issues {
			if i.ResKey == "rule:"+id {
				mine = append(mine, FieldError{ResKey: i.ResKey, Field: i.Field, Reason: i.Reason})
			}
		}
		if len(mine) > 0 {
			FailValidation(c, "访问规则未通过校验", mine)
			return
		}
	}

	if err := s.store.UpsertRule(ctx, req.Rule, req.Secret, s.sealer); err != nil {
		s.log.Error("保存访问规则失败", "err", err)
		Fail(c, CodeDownstream, "保存访问规则失败")
		return
	}
	OK(c, gin.H{"id": id})
}

func (s *Server) handleGetPolicy(c *gin.Context) {
	id := c.Param("id")
	if !store.IsPolicyID(id) {
		Fail(c, CodeNotFound, "只有 tls 与 log 两条全局策略")
		return
	}
	p, err := s.store.GetPolicy(c.Request.Context(), id)
	if err != nil {
		s.log.Error("读取全局策略失败", "err", err)
		Fail(c, CodeDownstream, "读取全局策略失败")
		return
	}
	// **用渲染器的默认值补齐缺的字段。**
	//
	// 返回字面意义的空 spec 会让界面无从说出真相：三个枚举一个都没选中、
	// 开关全 off，而人无从知道此刻节点上究竟什么在生效。补齐之后，
	// 界面显示的就是实际会被渲染下去的那一份。
	//
	// 默认值只有一份，在 render 包里；契约 §6.3 的 seed 必须与它一致，
	// 不一致就是文档和实现各说各话。
	filled, err := fillPolicyDefaults(id, p.Spec)
	if err != nil {
		s.log.Error("补齐策略默认值失败", "err", err)
		Fail(c, CodeDownstream, "读取全局策略失败")
		return
	}
	p.Spec = filled
	OK(c, p)
}

func (s *Server) handlePutPolicy(c *gin.Context) {
	id := c.Param("id")
	setAuditTarget(c, id)

	if !store.IsPolicyID(id) {
		Fail(c, CodeNotFound, "只有 tls 与 log 两条全局策略")
		return
	}
	var p model.Policy
	if err := c.ShouldBindJSON(&p); err != nil {
		Fail(c, CodeBadParam, "请求格式错误")
		return
	}
	p.ID = id
	if len(p.Spec) == 0 || !json.Valid(p.Spec) {
		Fail(c, CodeBadParam, "spec 必须是合法 JSON 对象")
		return
	}
	if err := s.store.UpsertPolicy(c.Request.Context(), p); err != nil {
		s.log.Error("保存全局策略失败", "err", err)
		Fail(c, CodeDownstream, "保存全局策略失败")
		return
	}
	OK(c, gin.H{"id": id})
}

// fillPolicyDefaults 把 spec 里缺的字段补成渲染器的默认值。
func fillPolicyDefaults(id string, spec json.RawMessage) (json.RawMessage, error) {
	pol, err := render.ParsePolicies(nil, nil)
	if err != nil {
		return nil, err
	}
	switch id {
	case model.PolicyTLS:
		if len(spec) > 0 {
			if err := json.Unmarshal(spec, &pol.TLS); err != nil {
				return nil, err
			}
		}
		return json.Marshal(pol.TLS)
	case model.PolicyLog:
		if len(spec) > 0 {
			if err := json.Unmarshal(spec, &pol.Log); err != nil {
				return nil, err
			}
		}
		return json.Marshal(pol.Log)
	}
	return spec, nil
}
