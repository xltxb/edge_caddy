package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/dnsops"
	"github.com/xltxb/edge_caddy/internal/store"
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

// errNodeDrained 表示这次操作对一台已下线的机器说不通。
//
// 单独一个错误：调用方据此报「状态冲突」而不是「下游失败」——
// 后者会让人去查网络、查凭证，而问题是他自己十分钟前把那台机器下线了。
var errNodeDrained = errors.New("节点已下线")

type dnsToggleReq struct {
	Enabled *bool `json:"enabled"`
}

// setNodeDNS 改一个节点的解析标志位，**并把安排真的同步到服务商**。
//
// 返回的 synced 说的是「解析真的变了」，不是「标志位写成功了」。这个区分是
// 这个函数存在的全部理由：调用方拿到的若是后者，它迟早会把它当成前者报出去。
//
// 两个调用方——手动开关与下线——必须走同一条路。它们原先各写各的：#21 把开关
// 那条接上了服务商，下线那条没跟着改，于是下线报「已停止解析」而解析纹丝未动。
// 只要「改标志位」和「同步服务商」是两个可以分开调的动作，就还会有第三个调用方
// 只调前一个。**把它们焊死在一个函数里，才是这个 bug 真正的修法。**
func (s *Server) setNodeDNS(ctx context.Context, nodeID string, enabled bool,
	reason, actor string) (synced bool, detail string, err error) {
	// **已下线的节点不能被重新放进解析。**
	//
	// 下线会关掉 dns_enabled，归一化因此自然排除了它——但那道排除是间接的，
	// 搭在另一个标志位的当前值上。点一下「恢复解析」标志位就回来了，
	// 而主控明确拒绝那台机器接入：解析于是指向一台连不上来的机器。
	//
	// **两个方向都挡。**
	//
	// 开的方向：解析会指向一台主控明确拒绝它接入的机器。
	//
	// 关的方向此前不挡，理由是「它不会把流量送过去」——那个理由仍然成立，
	// 但它漏了一件事：**关这一下会把 dns_reason 从 drained 改写成 manual**，
	// 而 drained_at 还在。于是节点页说「已下线」，DNS 页说「人手动关的」，
	// **两句单独看都对，只有并排才看得出对不上账**。
	//
	// 而对一台已下线的机器点「暂停解析」本来就说不通：它已经不在解析里了。
	if drained, err := s.store.IsNodeDrained(ctx, nodeID); err != nil {
		return false, "查下线状态失败：" + err.Error(), err
	} else if drained {
		if enabled {
			return false, "该节点已被下线，先「重新上线」再恢复解析", errNodeDrained
		}
		return false, "该节点已被下线，本来就不在解析里", errNodeDrained
	}
	if err := s.store.SetNodeDNS(ctx, nodeID, enabled, reason, actor); err != nil {
		return false, "改解析标志位失败：" + err.Error(), err
	}
	if s.dns == nil {
		return false, "尚未配置 DNS 服务商，解析未变动", nil
	}
	switch err := s.dns.Sync(ctx, nil); {
	case errors.Is(err, dnsops.ErrNoProvider):
		// 没配服务商不是错误：标志位仍然有意义（它决定归一化里谁参与）。
		// 但**必须说出来**，否则人以为解析已经变了。
		return false, "尚未配置 DNS 服务商，解析未变动", nil
	case err != nil:
		return false, "标志位已改，但同步到 DNS 服务商失败：" + err.Error(), nil
	default:
		return true, "解析安排已同步到服务商", nil
	}
}

func (s *Server) handleNodeDNS(c *gin.Context) {
	nodeID := c.Param("id")
	setAuditTarget(c, nodeID)

	var req dnsToggleReq
	// 两件事分开报：「字段名写错了」要改的是 key，「没带 enabled」要加的是字段。
	//
	// 并在一个条件里的话，两种情况得到同一句话，而它们的处置不一样。
	if err := bindStrict(c, &req); err != nil {
		Fail(c, CodeBadParam, err.Error())
		return
	}
	if req.Enabled == nil {
		Fail(c, CodeBadParam, "需要 enabled 字段：true 恢复解析，false 暂停解析")
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
	// 人在控制台点的，记下是谁 —— 半夜看到一台机器不接流量时，
	// 「谁关的」和「什么时候关的」是最先要问的两件事。
	synced, detail, err := s.setNodeDNS(ctx, nodeID, *req.Enabled, store.DNSManual, operatorOf(c))
	switch {
	case errors.Is(err, errNodeDrained):
		// 不是下游故障，是一次说不通的操作 —— 措辞要让人知道下一步该做什么。
		Fail(c, CodeStateConflict, detail)
		return
	case err != nil:
		s.log.Error("切换解析失败", "node", nodeID, "err", err)
		Fail(c, CodeDownstream, "切换解析失败")
		return
	}
	if !synced && s.dns != nil {
		setAuditPartial(c, detail)
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
	if err := bindStrict(c, &req); err != nil {
		Fail(c, CodeBadParam, err.Error())
		return
	}
	// 必须显式确认。下线会让一台机器彻底退出，误点的代价不对称。
	//
	// **这一条要单独判**：把它和绑定失败并在一个条件里，
	// 「confirm 拼错了」就会得到「需要显式确认」——而那句话会让人再点一次确认，
	// 方向正好是错的。
	if !req.Confirm {
		Fail(c, CodeBadParam, "下线需要显式确认（confirm: true）")
		return
	}

	ctx := c.Request.Context()
	steps := []gin.H{}

	// **ok 说的是解析真的变了，不是标志位写成功了。**
	//
	// 这一步原先拿 SetNodeDNS 的 err 当成功判据，于是没配服务商时也报 ok=true。
	// 另外两步诚实地报 false，唯独最要紧的这一步撒谎——运维看到「已停止解析」
	// 就去关机器，而流量还在往那台机器上打。
	synced, detail, err := s.setNodeDNS(ctx, nodeID, false, store.DNSDrained, operatorOf(c))
	if err != nil {
		detail = err.Error()
	}
	steps = append(steps, step("dns_removed", synced, detail))

	// **排空要在断隧道之前。** 断了就没法再问节点还剩多少连接了。
	//
	// 也要在摘解析之后：解析还指着这台机器的时候排空，是在排一个还在进水的池子。
	// 顺序是这三步唯一的硬约束。
	drained := s.drainStep(ctx, nodeID, synced)
	steps = append(steps, drained)

	// **先落下线标记，再断隧道。** 反过来的话，断开与写库之间有一个窗口，
	// 而 Agent 恰恰在那个窗口里重连 —— 它会被放进来，然后一直待到下次有人再点。
	if err := s.store.SetNodeDrained(ctx, nodeID, true, operatorOf(c)); err != nil {
		s.log.Error("落下线标记失败", "node", nodeID, "err", err)
		steps = append(steps, step("tunnel_closed", false, "落下线标记失败："+err.Error()))
		setAuditPartial(c, "下线标记没落成，节点会自己连回来")
		OK(c, gin.H{"steps": steps})
		return
	}
	closed := s.tunnel != nil && s.tunnel.Disconnect(nodeID)
	detail = "隧道已断开，此后拒绝该节点重连"
	if !closed {
		// 节点本来就不在线也算数：要紧的是**此后拒绝它重连**，
		// 而那一条已经落库了。报 true 是诚实的。
		detail = "节点当时不在线；下线标记已落，此后拒绝该节点重连"
	}
	steps = append(steps, step("tunnel_closed", true, detail))

	// **只有真的有步骤没成，才算部分成功。**
	//
	// 这里原先无条件记 partial，detail 还是一句写死的「连接未主动排空」——
	// 而同一次请求的响应里 conns_drained.ok 可能正是 true。于是审计表里这一列
	// 恒为 partial，等于不作数，而审计是 ADR-0013 那个准入模型的三分之一
	// （issue #45）。
	//
	// detail 取那一步自己的结论，不另写一句：两处各自措辞迟早会对不上账，
	// 而对不上的那一刻没有任何东西会说出来。
	if !stepOK(drained) {
		setAuditPartial(c, stepDetail(drained))
	} else if !synced {
		setAuditPartial(c, stepDetail(steps[0]))
	}
	OK(c, gin.H{"steps": steps})
}

// stepOK / stepDetail 读一个步骤自己报的结论。
//
// 从步骤里读而不是另写一句：两处各自措辞迟早会对不上账，
// 而对不上的那一刻没有任何东西会说出来。
func stepOK(s gin.H) bool {
	v, _ := s["ok"].(bool)
	return v
}

func stepDetail(s gin.H) string {
	v, _ := s["detail"].(string)
	return v
}

// handleNodeRejoin 撤销下线。
//
// 没有这个端点，下线就是个单程操作 —— 人只能去数据库改字段。
// 而下线是个会被误点的按钮（它就在节点卡片上），单程的误点代价太大。
func (s *Server) handleNodeRejoin(c *gin.Context) {
	nodeID := c.Param("id")
	setAuditTarget(c, nodeID)

	if err := s.store.SetNodeDrained(c.Request.Context(), nodeID, false, operatorOf(c)); err != nil {
		s.log.Error("重新上线失败", "node", nodeID, "err", err)
		Fail(c, CodeDownstream, "重新上线失败")
		return
	}
	// **解析不自动打开。** 一台机器能接入不等于它该马上分流量：它刚回来，
	// 配置可能还是旧的。解析由人另外点，或者由下一次成功下发带起来。
	OK(c, gin.H{
		"id":          nodeID,
		"drained_at":  nil,
		"dns_enabled": false,
		"detail":      "已允许重新接入；解析仍是关闭的，确认配置无误后再打开",
	})
}

// drainConnsTimeout 是等排空的上限。
//
// 不做成可配的：这个值要变的场景（一台连接特别多的机器）恰恰是人在旁边看着的
// 场景，而那时他要的是「还剩多少」这个数字，不是再等久一点——
// 拿到数字之后他自己会决定是关机还是再点一次。
const drainConnsTimeout = 30 * time.Second

// drainStep 让节点排空已建立的连接，并把结果说成人能据此做决定的样子。
func (s *Server) drainStep(ctx context.Context, nodeID string, dnsRemoved bool) gin.H {
	// 这个判断与下面那句「已建立的连接都已结束」的措辞，各有一条探针盯着
	// （scripts/probes.py 的「下线-排空真的跑到了」「下线-排空要说清它的边界」）。
	if !dnsRemoved {
		// 解析还指着这台机器，新连接源源不断，排空没有意义。
		// 说清是「跳过」而不是「失败」——后者会让人去查节点。
		return step("conns_drained", false, "上一步没能停掉解析，跳过排空（还在进水的池子排不干净）")
	}
	if s.tunnel == nil {
		return step("conns_drained", false, "隧道未装配")
	}

	out, err := s.tunnel.Drain(ctx, nodeID, drainConnsTimeout)
	if err != nil {
		// 节点本来就不在线是最常见的一种：那时排空既做不成也不必做，
		// 但**不能报成功**——「没连接可排」和「排干净了」在下一步（关机）
		// 面前是一回事，在这一步的语义上不是。
		return step("conns_drained", false, "节点不可达，连接未排空："+err.Error())
	}
	if !out.Drained {
		return step("conns_drained", false, fmt.Sprintf(
			"等了 %s 仍有 %d 条连接未结束；关机会掐断它们",
			drainConnsTimeout, out.Remaining))
	}
	// **说清这句话的边界。** 解析摘掉了，但 DNS 有 TTL，缓存在各级递归里，
	// 一段时间内仍会有新连接进来。「排空完成」指的是那一刻的连接数，
	// 不是「再也没有请求」——不说的话这是第三句假话。
	return step("conns_drained", true,
		"已建立的连接都已结束；解析缓存未过期前仍可能有新连接进来")
}

func step(name string, ok bool, detail string) gin.H {
	return gin.H{"step": name, "ok": ok, "detail": detail}
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
