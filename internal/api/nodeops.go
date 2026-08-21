package api

import (
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/dnsops"
)

func queryInt(c *gin.Context, key string, def int) int {
	v, err := strconv.Atoi(c.DefaultQuery(key, ""))
	if err != nil {
		return def
	}
	return v
}

// handleNodePush 把当前基线重推给单个节点。
//
// 这是 ADR-0005 的兜底：Caddy 拒绝的配置不自动重试，环境类临时故障
// （端口被别的进程短暂占用之类）由人在这里手动恢复。
func (s *Server) handleNodePush(c *gin.Context) {
	nodeID := c.Param("id")
	setAuditTarget(c, nodeID)

	if s.deployer == nil {
		Fail(c, CodeStateConflict, "下发调度器未装配")
		return
	}
	if !s.isOnline(nodeID) {
		Fail(c, CodeStateConflict, "节点不在线")
		return
	}

	cfgVersion, detail, issues, err := s.deployer.RepushNode(c.Request.Context(), nodeID)
	if len(issues) > 0 {
		FailValidation(c, "当前基线渲染不出可下发的配置", toFieldErrors(issues))
		return
	}
	if err != nil {
		s.log.Error("重推失败", "node", nodeID, "err", err)
		Fail(c, CodeNodeUnreachable, "重推失败："+err.Error())
		return
	}
	// 重推推的是**当前基线那一版**，不是新版本：把一台掉队的机器带上来，
	// 不该在下发记录里多出一次谁也没发起过的下发。
	OK(c, gin.H{"cfg_version": cfgVersion, "detail": detail})
}

type dnsToggleReq struct {
	Enabled *bool `json:"enabled"`
}

func (s *Server) handleNodeDNS(c *gin.Context) {
	nodeID := c.Param("id")
	setAuditTarget(c, nodeID)

	var req dnsToggleReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		Fail(c, CodeBadParam, "请求格式错误，需要 enabled 字段")
		return
	}
	// 动作名要跟着方向变：审计页上「暂停解析」和「恢复解析」是两件事，
	// 记成同一个动作等于把一半信息丢在这一步（api-contract §5）。
	if *req.Enabled {
		c.Set(ctxKeyAction, "恢复解析")
	} else {
		c.Set(ctxKeyAction, "暂停解析")
	}

	ctx := c.Request.Context()
	if err := s.store.SetNodeDNS(ctx, nodeID, *req.Enabled); err != nil {
		s.log.Error("切换解析失败", "node", nodeID, "err", err)
		Fail(c, CodeDownstream, "切换解析失败")
		return
	}

	// **改完标志位要真的同步到服务商。**
	//
	// 这里原先只改标志位，注释写着「真正调服务商属于 #21」——而 #21 完成之后
	// 这句话没跟着改。于是心跳超时的**自动**摘除会同步服务商，人手动点
	// 「暂停解析」却不会：同一件事两条路径行为不一致，而不一致的那条恰恰是
	// 人主动做的那条。一个什么也不做的开关比没有这个开关更糟。
	synced := false
	if s.dns != nil {
		switch err := s.dns.Sync(ctx, nil); {
		case errors.Is(err, dnsops.ErrNoProvider):
			// 没配服务商不是错误：标志位仍然有意义（它决定归一化里谁参与）。
			// 但**必须说出来**，否则人以为解析已经变了。
		case err != nil:
			s.log.Error("同步解析失败", "node", nodeID, "err", err)
			setAuditPartial(c, "标志位已改，但同步到 DNS 服务商失败："+err.Error())
			OK(c, gin.H{
				"id": nodeID, "dns_enabled": *req.Enabled, "dns_synced": false,
				"detail": "标志位已改，但同步到 DNS 服务商失败：" + err.Error(),
			})
			return
		default:
			synced = true
		}
	}

	detail := "解析安排已同步到服务商"
	if !synced {
		detail = "尚未配置 DNS 服务商，解析未变动"
	}
	OK(c, gin.H{"id": nodeID, "dns_enabled": *req.Enabled, "dns_synced": synced, "detail": detail})
}

func (s *Server) handleNodeProbe(c *gin.Context) {
	nodeID := c.Param("id")
	if s.tunnel == nil {
		Fail(c, CodeStateConflict, "隧道未装配")
		return
	}
	out, err := s.tunnel.Probe(c.Request.Context(), nodeID, 5*time.Second)
	if err != nil {
		// 探不通不是服务端错误，是关于节点的一个事实。
		Fail(c, CodeNodeUnreachable, "节点不可达")
		return
	}
	OK(c, gin.H{
		"reachable": true,
		"rtt_ms":    out.RTT.Milliseconds(),
		// 隧道通而 Admin 不通，说明 Caddy 挂了而 Agent 还活着 ——
		// 这两种故障的处置完全不同，所以分开报（契约 §4）。
		"caddy_admin": out.CaddyAdmin,
		"cfg_version": out.CfgVersion,
	})
}

type drainReq struct {
	Confirm bool `json:"confirm"`
}

// handleNodeDrain 下线一个节点。三步，逐步回报做了什么。
func (s *Server) handleNodeDrain(c *gin.Context) {
	nodeID := c.Param("id")
	setAuditTarget(c, nodeID)

	var req drainReq
	if err := c.ShouldBindJSON(&req); err != nil || !req.Confirm {
		// 必须显式确认。下线会让一台机器彻底退出，误点的代价不对称。
		Fail(c, CodeBadParam, "下线需要显式确认（confirm: true）")
		return
	}

	ctx := c.Request.Context()
	steps := []gin.H{}

	err := s.store.SetNodeDNS(ctx, nodeID, false)
	steps = append(steps, step("dns_removed", err == nil, detailOf(err,
		"已停止解析（真正调用 DNS 服务商属于 #21）")))

	// 连接排空与关闭隧道属于后续工单：这里如实说没做，
	// 而不是回一个 ok=true 让人以为流量已经排干净了。
	steps = append(steps, step("conns_drained", false, "尚未实现，连接不会被主动排空"))
	steps = append(steps, step("tunnel_closed", false, "尚未实现，隧道仍然保持"))

	setAuditPartial(c, "仅停止解析，排空与断隧道尚未实现")
	OK(c, gin.H{"steps": steps})
}

func step(name string, ok bool, detail string) gin.H {
	return gin.H{"step": name, "ok": ok, "detail": detail}
}

func detailOf(err error, okMsg string) string {
	if err != nil {
		return err.Error()
	}
	return okMsg
}

func (s *Server) isOnline(nodeID string) bool {
	if s.tunnel == nil {
		return false
	}
	for _, id := range s.tunnel.OnlineNodes() {
		if id == nodeID {
			return true
		}
	}
	return false
}
