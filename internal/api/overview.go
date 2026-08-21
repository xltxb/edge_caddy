package api

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/store"
)

type eventResp struct {
	ID   int64  `json:"id"`
	At   string `json:"at"`
	Node string `json:"node"`
	Kind string `json:"kind"`
	Msg  string `json:"msg"`
}

func (s *Server) handleOverview(c *gin.Context) {
	ctx := c.Request.Context()

	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.log.Error("读取节点失败", "err", err)
		Fail(c, CodeDownstream, "读取总览失败")
		return
	}
	baseline, err := s.store.Baseline(ctx)
	if err != nil {
		s.log.Error("读取基线失败", "err", err)
		Fail(c, CodeDownstream, "读取总览失败")
		return
	}
	events, err := s.store.RecentEvents(ctx, 40)
	if err != nil {
		s.log.Error("读取事件失败", "err", err)
		Fail(c, CodeDownstream, "读取总览失败")
		return
	}

	// 三档由同一条语句产出，保证 在线 + 异常 + 离线 == 总数。
	// 分别推导迟早会算不平，而一处口径错会在界面上冒出来两次。
	okCount, warnCount, downCount, total, err := s.store.CountNodesByStatus(ctx)
	if err != nil {
		s.log.Error("统计节点状态失败", "err", err)
		Fail(c, CodeDownstream, "读取总览失败")
		return
	}

	var driftCount int
	var connsTotal uint64
	var reqTotal, originTotal uint64
	for _, n := range nodes {
		if baseline != "" && n.CfgVersion != baseline {
			driftCount++
		}
		if s.health != nil {
			if m, ok := s.health.Latest(n.ID); ok {
				connsTotal += uint64(m.Conns)
				reqTotal += m.ReqTotal
				originTotal += m.OriginTotal
			}
		}
	}

	kpi := gin.H{
		// **在线只算 status == ok，不含 warn。**
		//
		// warn 是「连着但不健康」。把它算进在线，KPI 会在一台 CPU 81%、
		// 内存快满的机器上仍然显示绿色——而巡检时最该被看见的恰恰是那台。
		// 而且账要算得平：在线 + 异常 + 离线 == 总数，否则那几个异常节点
		// 会既被算进在线、又被单独点名，读的人两种理解都对不上另一半。
		"nodes_online": okCount,
		"nodes_warn":   warnCount,
		"nodes_down":   downCount,
		"nodes_total":  total,
		"conns_total":  connsTotal,
		// 「较昨日同时段」需要至少 24 小时历史；不足时给 null 而不是 0 ——
		// 0 会被读成「持平」（api-contract §3）。traffic_samples 的采集与
		// 同比计算在本切片之后补齐。
		"conns_delta_pct": nil,
		"origin_rate":     originRate(reqTotal, originTotal),
		// 配置漂移**只比对版本号**，不检查节点上的配置内容（ADR-0002）。
		// 「全部一致」的含义只是「最近一次下发都到达了」，不是「没人 SSH 上去改过」。
		"drift_nodes": driftCount,
	}

	items := make([]eventResp, 0, len(events))
	for _, e := range events {
		items = append(items, eventResp{
			ID: e.ID, At: e.CreatedAt.Format(time.RFC3339),
			Node: e.Node, Kind: e.Kind, Msg: e.Msg,
		})
	}

	OK(c, gin.H{"baseline": baseline, "kpi": kpi, "events": items})
}

// originRate 是**到达 upstream 的请求 ÷ 边缘收到的总请求**，越低越好。
//
// 注意它不是缓存命中率——官方 Caddy 没有 HTTP 缓存模块。没到达 upstream 的
// 那部分，是被访问规则拦下或由静态响应处理掉的（api-contract §3）。
//
// 一个请求都还没有时返回 null 而不是 0：0 会被读成「一个请求都没回源」，
// 那是个很好的数字，而真相是「还没有数据」。
func originRate(req, origin uint64) any {
	if req == 0 {
		return nil
	}
	return float64(int(float64(origin)/float64(req)*1000+0.5)) / 10
}

func (s *Server) handleAudit(c *gin.Context) {
	limit := queryInt(c, "limit", 50)
	before := int64(queryInt(c, "before_id", 0))
	operator := c.Query("operator")

	items, err := s.store.ListAudit(c.Request.Context(), operator, limit, before)
	if err != nil {
		s.log.Error("读取审计失败", "err", err)
		Fail(c, CodeDownstream, "读取审计失败")
		return
	}
	if items == nil {
		items = []store.AuditEntry{}
	}
	var next any
	if len(items) > 0 {
		next = items[len(items)-1].ID
	}
	OK(c, gin.H{"items": items, "next_before_id": next})
}
