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
	// dns_sync 是**常驻**的：界面上那个「已退出解析」徽标也是常驻的，
	// 而一次请求响应里的 dns_synced 会消失。一次失败的同步之后，
	// 没有这个字段的话徽标会一直说「这台机器不接流量了」——
	// 而它照旧在解析里。
	sync, err := s.store.GetDNSSync(ctx)
	if err != nil {
		s.log.Error("读取解析同步状态失败", "err", err)
		Fail(c, CodeDownstream, "读取节点失败")
		return
	}
	OK(c, gin.H{"items": items, "baseline": baseline, "dns_sync": sync})
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
	//
	// install_cmd 给的是**部署脚本**，不是裸的 edge-agent 命令。
	//
	// 裸命令跑得起来，而且跑起来之后是前台进程、没有 systemd 单元、
	// **没有 Restart=always**——而受保护域名的 fail-closed 依赖 Agent 存活
	// （ADR-0003）：它挂掉那一刻那些域名整体 502，没有任何东西把它拉回来。
	//
	// 也就是说，发一条裸命令等于让人有机会省掉部署脚本存在的理由。
	// 这与「--ca-pin 必填、不给默认值」是同一条判据：一道真实的保护，
	// 不该留一个能绕过它的口子。
	//
	// --agent-bin 留在命令里并给占位，而不是省掉走默认值：它是唯一一个
	// 「你必须自己先办好」的参数，写在命令里比藏在文档里更难被跳过。
	OK(c, gin.H{
		"token":      plain,
		"expires_at": expires.Format(time.RFC3339),
		"ca_pin":     s.caPin,
		"install_cmd": fmt.Sprintf(
			"sudo ./edge-node.sh install --master %s --node-id %s --token %s --ca-pin %s --agent-bin ./edge-agent",
			s.masterAddr, req.NodeID, plain, s.caPin),

		// verify_cmd 与 install_cmd 一起给，不是可选的补充。
		//
		// 照「复制命令」按钮做的人不会自己想到还要跑一次 verify，而 verify 查的
		// 正是 Caddy Admin 有没有暴露在回环之外——私钥以 load_pem 内联在运行
		// 配置里（ADR-0010），能读 Admin 就能读到它们。
		//
		// **一道没有人会执行的检查，等于不存在。** 部署脚本里为「没在监听」和
		// 「监听错地方」专门分了两个返回值，而如果没人跑它，那个区分一次也用不上。
		"verify_cmd": "sudo ./edge-node.sh verify",

		// 这两个文件都得先在当前目录里。脚本自己也是相对路径——
		// 它和 edge-agent 是同一类东西：命令里指着它，谁也不负责送它上去。
		"prerequisites": []string{
			"当前目录下有 edge-node.sh（本仓库 deploy/ 目录）",
			"当前目录下有 edge-agent 二进制（脚本不负责下载）",
		},
	})
}
