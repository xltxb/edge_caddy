package api

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/store"
)

type nodeResp struct {
	ID         string  `json:"id"`
	City       string  `json:"city"`
	Vendor     string  `json:"vendor"`
	Line       string  `json:"line"`
	PublicIP   string  `json:"public_ip"`
	Status     string  `json:"status"`
	Online     bool    `json:"online"`
	CfgVersion string  `json:"cfg_version"`
	Drift      bool    `json:"drift"`
	DNSEnabled bool    `json:"dns_enabled"`
	LastHBAt   *string `json:"last_hb_at"`
	HBAgeMS    *int64  `json:"hb_age_ms"`
	CPU        float64 `json:"cpu"`
	Mem        float64 `json:"mem"`
	Conns      uint32  `json:"conns"`
	// Routes / Rules 是**该节点当前生效配置里**的数量，由心跳上报，
	// 不是全局数量。漂移的节点会报旧数字，那正是它有用的地方。
	Routes uint32 `json:"routes"`
	Rules  uint32 `json:"rules"`
	// CPUSeries 没有数据时是 null，不是一串 0 —— 0 会被读成「负载为零」。
	CPUSeries []int     `json:"cpu_series"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) handleListNodes(c *gin.Context) {
	ctx := c.Request.Context()

	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.log.Error("读取节点失败", "err", err)
		Fail(c, CodeDownstream, "读取节点失败")
		return
	}
	baseline, err := s.store.Baseline(ctx)
	if err != nil {
		s.log.Error("读取基线失败", "err", err)
		Fail(c, CodeDownstream, "读取基线失败")
		return
	}

	online := map[string]bool{}
	if s.tunnel != nil {
		for _, id := range s.tunnel.OnlineNodes() {
			online[id] = true
		}
	}

	items := make([]nodeResp, 0, len(nodes))
	for _, n := range nodes {
		item := nodeResp{
			ID: n.ID, City: n.City, Vendor: n.Vendor, Line: n.Line,
			PublicIP: n.PublicIP, Status: n.Status, Online: online[n.ID],
			CfgVersion: n.CfgVersion, DNSEnabled: n.DNSEnabled, CreatedAt: n.CreatedAt,
			// 配置漂移 = 节点上报的版本 ≠ 基线。**只比对版本号，不检查内容**
			// （ADR-0002）：有人 SSH 上去手改配置、或节点重启后回退，漂移不会亮。
			Drift: baseline != "" && n.CfgVersion != baseline,
		}
		if s.health != nil {
			item.CPUSeries = s.health.CPUSeries(n.ID)
			if m, ok := s.health.Latest(n.ID); ok {
				item.CPU, item.Mem, item.Conns = m.CPU, m.Mem, m.Conns
				item.Routes, item.Rules = m.Routes, m.Rules
			}
		}
		if n.LastHBAt != nil {
			ts := n.LastHBAt.Format(time.RFC3339)
			age := time.Since(*n.LastHBAt).Milliseconds()
			item.LastHBAt, item.HBAgeMS = &ts, &age
		}
		items = append(items, item)
	}
	OK(c, gin.H{"items": items, "baseline": baseline})
}

type tokenReq struct {
	NodeID   string `json:"node_id"`
	City     string `json:"city"`
	Vendor   string `json:"vendor"`
	Line     string `json:"line"`
	PublicIP string `json:"public_ip"`
}

func (s *Server) handleIssueToken(c *gin.Context) {
	var req tokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, CodeBadParam, "请求格式错误")
		return
	}
	if req.NodeID == "" {
		Fail(c, CodeBadParam, "node_id 不能为空")
		return
	}
	setAuditTarget(c, req.NodeID)

	plain, expires, err := s.store.IssueEnrollToken(c.Request.Context(), store.NodeSpec{
		NodeID: req.NodeID, City: req.City, Vendor: req.Vendor,
		Line: req.Line, PublicIP: req.PublicIP,
	})
	if err != nil {
		s.log.Error("签发接入 Token 失败", "err", err)
		Fail(c, CodeDownstream, "签发接入 Token 失败")
		return
	}

	// Token 明文只在这一次响应里出现，任何后续接口都不回显（PRD §7）。
	OK(c, gin.H{
		"token":      plain,
		"expires_at": expires.Format(time.RFC3339),
		"ca_pin":     s.caPin,
		"install_cmd": fmt.Sprintf(
			"edge-agent --master %s --node-id %s --token %s --ca-pin %s",
			s.masterAddr, req.NodeID, plain, s.caPin),
	})
}
