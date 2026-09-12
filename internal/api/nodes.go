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
	// DrainedAt 非 null 表示这台机器是**被人下线的**（ADR-0014）。
	// 它和 Status 是两个正交的事实：可以「已下线且在线」（刚下线，隧道还没断干净），
	// 也可以「未下线但离线」（它自己挂了）。前端别把它们合成一个徽标。
	DrainedAt *string `json:"drained_at"`
	// **「未参与解析」要说得出为什么。**
	//
	// 三条路径关掉解析（人手动、系统自动摘、人下线），而它们的处置完全不同：
	// 自己关的想开就开，系统摘的要先去修那台机器，下线的要先「重新上线」。
	// 只有一个 dns_enabled 的时候，界面只能说「未参与解析」四个字。
	// Reconnects1h 是**过去一小时这条隧道断了又接上几次**。
	//
	// 它存在的理由是**去抖会把真故障吃掉**：断开到重连只要 1–2 秒，
	// 而离线判定要连续错过 heartbeat_interval × offline_threshold（默认 9 秒）
	// 才翻 down —— 所以一条每十分钟断一次的隧道，
	// 在 status / online / hb_age_ms 三个瞬时值上**全部是健康的**。
	//
	// 灰度上真发生过：CDN 每隔十几分钟切一次长连接，界面上看不出任何异常，
	// 唯一的痕迹在那台机器的 Agent 日志里。
	//
	// **区分「一次抖动」和「反复抖动」需要的不是更灵敏的判定，
	// 是一个跨时间的计数** —— 前者不该惊动人，后者是故障。
	//
	// **数不出来时是 null，不是 0。** 这不只是契约 §0.4 的通则，
	// 这个字段有它自己的一条：
	//
	//	它的存在理由就是「在一切看起来正常时指出异常」，
	//	而 0 恰好是「一切正常」的样子。
	//
	// 给 0 等于让它在自己失效的那一刻**伪装成它最想否定的那个状态**。
	// 别的字段退化成 0 只是丢信息，这一个退化成 0 是主动说反话。
	//
	// 「那条错误会进日志」补救不到位：日志在主控上，看界面的人在浏览器里。
	// 它保护的是事后排查的人，而**此刻正在看这台节点健不健康的那个人，
	// 正是这个字段唯一的服务对象**。
	//
	// 同一条判据在这个仓库里已经用过四次：conns_delta_pct（历史不足）、
	// origin_rate（还没有样本）、cpu_series（主控刚重启）、
	// dns_actor（系统自动摘的，不是 "system"）。
	Reconnects1h *int    `json:"reconnects_1h"`
	DNSReason    string  `json:"dns_reason"` // manual | auto_offline | drained
	DNSActor     *string `json:"dns_actor"`  // 操作人；系统自动摘除时是 null
	DNSChangedAt *string `json:"dns_changed_at"`
	// AgentVersion 是节点上跑的 Agent 版本。空串表示这台机器还没接入过。
	AgentVersion string `json:"agent_version"`

	// GeoDBOK 说这台节点上的 GeoIP 库跟不跟得上主控那份。
	//
	//	true   跟上了
	//	false  **没有库、或者是旧的** —— 它上面的地域规则不生效
	//	null   主控自己就没有库（没在用地域功能），这个问题不适用
	//
	// **null 不是 false**（§0.4）：一个没在用地域功能的系统，
	// 每台节点都标红是在报告一个不存在的问题，而人两天就学会忽略它。
	GeoDBOK *bool `json:"geo_db_ok"`

	// BlockedLastHour 是过去一小时被访问规则拦下的请求数。
	//
	// **`null` 是「还不知道」，不是 0。** 节点刚接入、或者 Agent 刚重启时
	// 还没有可比的两次心跳 —— 那时回 0 会被读成「一个都没拦」，
	// 而它要回答的问题恰恰是「此刻在不在被打」，**0 正是「没被打」的样子**。
	// 与 reconnects_1h 是同一条理由。
	//
	// **它数不到 `abort` 那一档**：静默断连不产生响应，Caddy 的按状态码计数
	// 里没有它。要看得见拦了多少，路由的处置方式得是 403 或 404。
	//
	// 窗口在主控内存里，**主控重启后从 0 重新攒** —— 与 cpu_series 同一条路。
	BlockedLastHour *uint64 `json:"blocked_1h"`

	// InRotation 是「**这台机器现在拿不拿得到流量**」，WeightSet 是
	// 「有没有人给它配过权重」。
	//
	// 这两个字段存在的理由是**三个绿的加起来会给人一个假印象**：
	// 在线 ✓、配置已下发 ✓、参与解析 ✓ —— 而它一份流量也没有，
	// 因为没人分配过权重。每个字段各自都对。
	//
	// 判据在 dnssched.Build 里（`dns_enabled && status != down && weight > 0`），
	// **由后端算好给出，不让前端自己推**：前端重推一遍就是两处知识，
	// 判据哪天变了只会改一处，症状是界面说「在解析里」而服务商上没有它。
	//
	// **`null` 是「算不出来」**，不是 false：没配 DNS 服务商、或者读解析
	// 安排失败时是这一档。回 false 会被读成「这台机器不承载流量」——
	// 那是一句关于机器的断言，而真相是关于我们自己的（我们不知道）。
	//
	// weight_set 与 `weight == 0` 不是一回事：从没有人过目（新接入）
	// 与人有意设成 0 是两件事，界面的警告只该出现在前者上。
	InRotation *bool `json:"in_rotation"`
	WeightSet  *bool `json:"weight_set"`
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

	// 主控那份库的哈希。**读不到就当没有**：这一列是附加信息，
	// 为它把整页变成错误页不成比例，而 null 恰好表达「这个问题不适用」。
	masterGeoSHA := ""
	if g, err := s.store.GetGeoDB(ctx, false); err == nil {
		masterGeoSHA = g.SHA256
	}

	// 重连次数**一次查回全体**，不在下面的循环里逐台问。
	//
	// 逐台问是 N 个往返，而这个页面是常驻轮询的；events 又是只写不清的表
	// （#34），行数随时间线性涨。ctx 一超时，reconnects_1h 会从某一行开始
	// 整片变 null ——恰好是这个字段最该说话的时候（issue #59）。
	//
	// 失败时整张表留 nil，下面那一列就是 null（「我们不知道」）。并且照样
	// 进日志：日志给事后排查的人，null 给此刻在看界面的人，两个都要。
	var reconnects map[string]int
	if m, err := s.store.CountReconnectsByNode(ctx, time.Hour); err != nil {
		s.log.Error("统计重连次数失败", "err", err)
	} else {
		reconnects = m
	}

	// 谁在解析轮换里、谁被人配过权重。**同一份计划，与解析页读的是同一个来源**
	// —— 两处各算各的迟早会给出两个答案。
	//
	// 算不出来（没配服务商、读库失败）时两张表都留空，下面的字段就是 null
	// ——「我们不知道」，而不是「这台机器不承载流量」。
	var inRotation, weightSet map[string]bool
	if s.dns != nil {
		if plan, err := s.dns.CurrentPlan(ctx, nil); err == nil {
			inRotation, weightSet = map[string]bool{}, map[string]bool{}
			for _, l := range plan.Lines {
				for _, e := range l.Entries {
					// **任意一条线上在轮换里，这台机器就在承载流量。**
					// 按线取或，因为节点页那一行说的是这台机器整体。
					if e.InRotation {
						inRotation[e.Node] = true
					}
					if e.WeightSet {
						weightSet[e.Node] = true
					}
					// 出现过就要有键 —— 否则「不在轮换里」与「不在这份计划里」
					// 在下面分不开，而后者根本不该发生。
					if _, ok := inRotation[e.Node]; !ok {
						inRotation[e.Node] = false
					}
					if _, ok := weightSet[e.Node]; !ok {
						weightSet[e.Node] = false
					}
				}
			}
		} else {
			s.log.Error("读取解析安排失败，节点页的 in_rotation 将是 null", "err", err)
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
		if inRotation != nil {
			v := inRotation[n.ID]
			item.InRotation = &v
		}
		if weightSet != nil {
			v := weightSet[n.ID]
			item.WeightSet = &v
		}
		if s.health != nil {
			item.CPUSeries = s.health.CPUSeries(n.ID)
			if v, ok := s.health.BlockedLastHour(n.ID); ok {
				item.BlockedLastHour = &v
			}
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
		if n.DrainedAt != nil {
			ts := n.DrainedAt.Format(time.RFC3339)
			item.DrainedAt = &ts
		}
		item.AgentVersion = n.AgentVersion
		if masterGeoSHA != "" {
			// **主控有库时这一列才有意义。** 没在用地域功能的系统里，
			// 每台节点都标红是在报告一个不存在的问题。
			ok := n.GeoDBSha == masterGeoSHA
			item.GeoDBOK = &ok
		}
		if reconnects != nil {
			c := reconnects[n.ID]
			item.Reconnects1h = &c
		}
		item.DNSReason = n.DNSReason
		if n.DNSActor != "" {
			// **系统自动摘除时是 null，不是「system」。**
			// 一个叫 system 的操作人会在界面上冒出一个不存在的账号，
			// 而人会去问那是谁。
			a := n.DNSActor
			item.DNSActor = &a
		}
		if n.DNSChangedAt != nil {
			ts := n.DNSChangedAt.Format(time.RFC3339)
			item.DNSChangedAt = &ts
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

type nodeLogResp struct {
	At    string `json:"at"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// handleNodeLogs 回一个节点最近的运行日志（#26，契约 §4）。
//
// **是 Agent 自己的运行日志，不是 Caddy 的 access log。** 人在这一栏问的是
// 「这台机器上发生了什么」——配置应用、证书加载、校验端点报错。
// access log 属于另一个问题（流量分析），而且它的量级会把隧道压垮。
func (s *Server) handleNodeLogs(c *gin.Context) {
	nodeID := c.Param("id")
	lines, err := s.store.ListNodeLogs(c.Request.Context(), nodeID, queryInt(c, "limit", 200))
	if err != nil {
		s.log.Error("读取节点日志失败", "node", nodeID, "err", err)
		Fail(c, CodeDownstream, "读取节点日志失败")
		return
	}
	items := make([]nodeLogResp, 0, len(lines))
	for _, l := range lines {
		items = append(items, nodeLogResp{
			At: l.At.Format(time.RFC3339), Level: l.Level, Msg: l.Msg,
		})
	}
	// 空列表与「这个节点不存在」在这里不区分：两者对界面是同一件事
	// （没有日志可显示），而节点存不存在 GET /nodes 已经答过了。
	OK(c, gin.H{"items": items})
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
	if err := bindStrict(c, &req); err != nil {
		Fail(c, CodeBadParam, err.Error())
		return
	}
	// 已下线的节点不该拿到新 Token。挡在这里是为了把话说明白 ——
	// 接入路径也会挡（tunnel.refuseIfDrained），但那时人已经跑完安装脚本了，
	// 而错误只出现在那台机器的日志里。
	if req.NodeID != "" {
		drained, err := s.store.IsNodeDrained(c.Request.Context(), req.NodeID)
		if err != nil {
			s.log.Error("查下线状态失败", "node", req.NodeID, "err", err)
			Fail(c, CodeDownstream, "签发接入 Token 失败")
			return
		}
		if drained {
			Fail(c, CodeStateConflict, "该节点已被下线，先「重新上线」再签发接入 Token")
			return
		}
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
