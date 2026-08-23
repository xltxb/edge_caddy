package api

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/dnsctl"
	"github.com/xltxb/edge_caddy/internal/dnsops"
	"github.com/xltxb/edge_caddy/internal/dnssched"
	"github.com/xltxb/edge_caddy/internal/store"
)

func (s *Server) handleGetDNSWeights(c *gin.Context) {
	if s.dns == nil {
		Fail(c, CodeStateConflict, "DNS 编排未装配")
		return
	}
	ctx := c.Request.Context()

	plan, err := s.dns.CurrentPlan(ctx, nil)
	if err != nil {
		s.log.Error("读取解析安排失败", "err", err)
		Fail(c, CodeDownstream, "读取解析安排失败")
		return
	}
	sync, err := s.store.GetDNSSync(ctx)
	if err != nil {
		s.log.Error("读取解析同步状态失败", "err", err)
		Fail(c, CodeDownstream, "读取解析安排失败")
		return
	}
	OK(c, gin.H{
		"domain": plan.Domain,
		"lines":  plan.Lines,
		// 最近一次同步的结果。它与 lines 里的 share 是两件事：
		// share 是**我们打算**怎么分，dns_sync 说的是**服务商那边真的这样了没有**。
		"dns_sync": sync,
		// capabilities 如实说出这家服务商做不到什么，界面据此把无效的输入框
		// 置灰并说明原因——而不是让人配了个没有效果的数字。
		"capabilities": s.dns.Caps(ctx),
	})
}

type weightsReq struct {
	Lines []struct {
		Code    string `json:"code"`
		Entries []struct {
			Node   string `json:"node"`
			Weight int    `json:"weight"`
		} `json:"entries"`
	} `json:"lines"`
}

// handlePutDNSWeights 保存权重并**立即**推到服务商。
//
// 顺序是先推后存：推失败就不落库（api-contract §8）。反过来的话，
// 库里会留下一份服务商上并不存在的安排，而界面照常显示它——
// 那是最糟的一种不一致，因为看起来一切正常。
func (s *Server) handlePutDNSWeights(c *gin.Context) {
	var req weightsReq
	if err := bindStrict(c, &req); err != nil {
		Fail(c, CodeBadParam, err.Error())
		return
	}
	if s.dns == nil {
		Fail(c, CodeStateConflict, "DNS 编排未装配")
		return
	}

	weights := dnssched.Weights{}
	var issues []FieldError
	for i, l := range req.Lines {
		if !dnssched.IsLine(l.Code) {
			issues = append(issues, FieldError{
				ResKey: "dns", Field: fieldPath("lines", i, "code"),
				Reason: "未知的线路码，只能是 ct / cu / cm / tw / ov",
			})
			continue
		}
		weights[l.Code] = map[string]int{}
		for j, e := range l.Entries {
			if e.Weight < 0 {
				issues = append(issues, FieldError{
					ResKey: "dns", Field: fieldPath2("lines", i, "entries", j, "weight"),
					Reason: "权重不能为负",
				})
				continue
			}
			weights[l.Code][e.Node] = e.Weight
		}
	}
	if len(issues) > 0 {
		FailValidation(c, "解析权重未通过校验", issues)
		return
	}

	ctx := c.Request.Context()
	setAuditTarget(c, "dns")

	// 先推。没配服务商时跳过推送但仍然保存——权重是本地的意图，
	// 没有服务商不代表不能先配好。
	err := s.dns.Sync(ctx, weights)
	switch {
	case errors.Is(err, dnsops.ErrNoProvider):
		s.log.Warn("尚未配置 DNS 服务商，权重只保存在本地")
	case err != nil:
		var capErr *dnsctl.ErrCapability
		if errors.As(err, &capErr) {
			// 能力不足要与「下游失败」分开：后者会让人去查网络、查凭证，
			// 而问题根本不在那儿。
			Fail(c, CodeBadParam, capErr.Reason)
			return
		}
		s.log.Error("同步解析失败", "err", err)
		Fail(c, CodeDownstream, "同步到 DNS 服务商失败："+err.Error())
		return
	}

	if err := s.store.PutDNSWeights(ctx, store.DNSWeights(weights)); err != nil {
		s.log.Error("保存权重失败", "err", err)
		Fail(c, CodeDownstream, "保存权重失败")
		return
	}

	plan, err := s.dns.CurrentPlan(ctx, weights)
	if err != nil {
		s.log.Error("读取解析安排失败", "err", err)
		Fail(c, CodeDownstream, "保存成功但读取失败")
		return
	}

	// **把这次推的结果一起回出去。**
	//
	// 这里原先只回 {domain, lines}：推成功了没有任何一句话说，
	// 而「尚未配置服务商」那一支只写了一行日志就往下走了——
	// 于是保存按钮在「推上去了」和「只存在本地」两种情况下**长得一模一样**。
	//
	// 答案该出现在按下按钮的地方。它与 GET 那边同源（都读 dns_sync），
	// 所以不会出现「保存时说推上去了、刷新后徽标说没有」这种自相矛盾。
	sync, serr := s.store.GetDNSSync(ctx)
	if serr != nil {
		s.log.Error("读取解析同步状态失败", "err", serr)
	}
	OK(c, gin.H{"domain": plan.Domain, "lines": plan.Lines, "dns_sync": sync})
}

func fieldPath(a string, i int, b string) string {
	return a + "[" + itoaN(i) + "]." + b
}

func fieldPath2(a string, i int, b string, j int, c string) string {
	return a + "[" + itoaN(i) + "]." + b + "[" + itoaN(j) + "]." + c
}

func itoaN(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}
