package api

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/deploy"
	"github.com/xltxb/edge_caddy/internal/store"
)

type deployReq struct {
	ResKeys []string `json:"res_keys"`
}

func (s *Server) handleDeploy(c *gin.Context) {
	var req deployReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, CodeBadParam, "请求格式错误")
		return
	}
	if s.deployer == nil {
		Fail(c, CodeStateConflict, "下发调度器未装配")
		return
	}

	p, _ := principalOf(c)
	res, issues, err := s.deployer.Deploy(c.Request.Context(), p.Name, req.ResKeys)

	switch {
	case errors.Is(err, deploy.ErrNoOnlineNodes):
		// 静默成功会让人以为配置生效了，而实际上一台机器都没收到。
		Fail(c, CodeStateConflict, "没有在线节点，本次下发未执行")
		return
	case err != nil:
		s.log.Error("下发失败", "err", err)
		Fail(c, CodeDownstream, "下发失败："+err.Error())
		return
	}

	if len(issues) > 0 {
		// 校验不过即整体拒绝，**一个节点都没被触达**。
		FailValidation(c, fmt.Sprintf("配置校验未通过，共 %d 处问题", len(issues)), toFieldErrors(issues))
		return
	}

	setAuditTarget(c, res.CfgVersion)
	if res.FailCount > 0 {
		// 部分成功记 partial，不记 ok 也不记 fail —— 那两者都是撒谎。
		setAuditPartial(c, fmt.Sprintf("%d 成功 / %d 失败", res.OKCount, res.FailCount))
	}

	OK(c, gin.H{
		"deploy_id":   res.DeployID,
		"cfg_version": res.CfgVersion,
		"targets":     res.Targets,
	})
}

func (s *Server) handleGetDeploy(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		Fail(c, CodeBadParam, "下发编号必须是整数")
		return
	}

	d, results, err := s.store.GetDeploy(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		// 资源不存在用 1003，不用 HTTP 404 —— 404 只表示端点没实现。
		Fail(c, CodeNotFound, "没有这次下发记录")
		return
	}
	if err != nil {
		s.log.Error("读取下发记录失败", "err", err)
		Fail(c, CodeDownstream, "读取下发记录失败")
		return
	}

	if results == nil {
		results = []store.DeployResult{}
	}
	OK(c, gin.H{
		"id": d.ID, "cfg_version": d.CfgVersion, "operator": d.Operator,
		"res_keys": d.ResKeys, "ok_count": d.OKCount, "fail_count": d.FailCount,
		// targets 与 target_count 是同一件事的两个投影，库里只存前者——
		// 存两份迟早会不一致。target_count 保留是因为前端已经在用它。
		"targets": d.Targets, "target_count": len(d.Targets),
		"is_baseline": d.IsBaseline, "created_at": d.CreatedAt,
		"phase": deployPhase(d.Targets, results), "results": results,
	})
}

// deployPhase —— 「结束了」的定义是**全部目标都回报了，且没有还会再动的**。
//
// 不能用「有节点回报过」来判断：那在还有节点在飞时会谎报为已完成。
// 也不能只看回报数：重试中的节点已经回报过一次失败，但它那一行还会变
// （ADR-0005 的分类落地在 #19，届时 retrying 会真的为 true）。
func deployPhase(targets []string, results []store.DeployResult) string {
	if len(targets) == 0 || len(results) < len(targets) {
		return "running"
	}
	for _, r := range results {
		if r.Retrying {
			return "running"
		}
	}
	return "done"
}

func (s *Server) handleListDeploys(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	before, _ := strconv.ParseInt(c.DefaultQuery("before_id", "0"), 10, 64)

	items, err := s.store.ListDeploys(c.Request.Context(), limit, before)
	if err != nil {
		s.log.Error("读取下发记录失败", "err", err)
		Fail(c, CodeDownstream, "读取下发记录失败")
		return
	}
	if items == nil {
		items = []store.Deploy{}
	}
	var next any
	if len(items) > 0 {
		next = items[len(items)-1].ID
	}
	OK(c, gin.H{"items": items, "next_before_id": next})
}
