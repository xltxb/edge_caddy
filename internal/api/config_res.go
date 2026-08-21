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

	if issues := render.Validate([]model.Route{r}); len(issues) > 0 {
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
