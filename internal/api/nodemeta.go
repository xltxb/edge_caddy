package api

import (
	"context"
	"errors"
	"net"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/dnsops"
	"github.com/xltxb/edge_caddy/internal/store"
)

type nodeMetaReq struct {
	City     string `json:"city"`
	Vendor   string `json:"vendor"`
	Line     string `json:"line"`
	PublicIP string `json:"public_ip"`
}

// handleUpdateNode 改一个节点的元数据（城市 / 机房 / 中转线路 / 公网 IP）。
//
// **`node_id` 不能改。** 它是这台机器的身份，写在隧道证书的 CN 里（ADR-0009）。
// 改它等于换一台机器，而那是「删掉再接一台」，不是「编辑」。
// 路径里的 id 是权威的，请求体里没有这个字段——bindStrict 会拒掉带它的请求，
// 而那句报错正好说清了原因。
//
// **status / dns_enabled / drained_at 也不能改。** 那些是观察和意图，
// 各有自己的写入路径（ADR-0014）。一个能改 status 的编辑接口，
// 会让人以为可以手工把一台死机器改成在线——而那台机器不会因此活过来。
func (s *Server) handleUpdateNode(c *gin.Context) {
	nodeID := c.Param("id")
	setAuditTarget(c, nodeID)

	var req nodeMetaReq
	if err := bindStrict(c, &req); err != nil {
		Fail(c, CodeBadParam, err.Error())
		return
	}

	var issues []FieldError
	if req.City == "" {
		issues = append(issues, FieldError{ResKey: nodeID, Field: "city", Reason: "不能为空"})
	}
	// **公网 IP 必须是合法 IP，而且这一条是承重的。**
	//
	// 它会被写进 DNS 记录。一个写错的值不会在这里出事，
	// 它会在下一次同步解析时把访问者送到一个不存在的地方——
	// 而那时人查的是 DNS 服务商，不是这个表单。
	if net.ParseIP(req.PublicIP) == nil {
		issues = append(issues, FieldError{
			ResKey: nodeID, Field: "public_ip",
			Reason: "要填一个合法的 IP —— 它会被写进 DNS 记录",
		})
	}
	if len(issues) > 0 {
		FailValidation(c, "节点信息未通过校验", issues)
		return
	}

	ctx := c.Request.Context()
	before, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			Fail(c, CodeNotFound, "没有这个节点")
			return
		}
		s.log.Error("读取节点失败", "node", nodeID, "err", err)
		Fail(c, CodeDownstream, "读取节点失败")
		return
	}

	if err := s.store.UpdateNodeMeta(ctx, store.NodeSpec{
		NodeID: nodeID, City: req.City, Vendor: req.Vendor,
		Line: req.Line, PublicIP: req.PublicIP,
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			Fail(c, CodeNotFound, "没有这个节点")
			return
		}
		s.log.Error("修改节点失败", "node", nodeID, "err", err)
		Fail(c, CodeDownstream, "修改节点失败")
		return
	}

	// **改了公网 IP 就必须把解析同步过去，并且如实说同步了没有。**
	//
	// 不同步的话，控制台显示的是新 IP、服务商那边还是旧的——
	// 而两边各自看起来都正常。这与「暂停解析只改标志位不推服务商」
	// 是同一个形状，那次的教训是：**返回的 ok 要说的是「解析真的变了」，
	// 不是「库里写成功了」。**
	// **按 IP 的值比，不按字符串比。**
	//
	// 库里存的是 inet，读出来经过 host() 归一化；表单里那个是人敲的。
	// IPv6 尤其：`2001:db8::1` 和 `2001:0db8:0:0:0:0:0:1` 是同一个地址，
	// 字符串却不同——那会让一次「只改了城市」的编辑去推一次解析，
	// 并回报「203.0.113.7 → 203.0.113.7，解析已同步」。
	// **一句字面为真、而读起来是假话的 detail**，比不说更坏。
	synced, detail := false, ""
	if !net.ParseIP(before.PublicIP).Equal(net.ParseIP(req.PublicIP)) {
		synced, detail = s.syncAfterIPChange(ctx, before.PublicIP, req.PublicIP)
	}

	OK(c, gin.H{
		"id": nodeID, "city": req.City, "vendor": req.Vendor,
		"line": req.Line, "public_ip": req.PublicIP,
		"dns_synced": synced, "detail": detail,
	})
}

func (s *Server) syncAfterIPChange(ctx context.Context, oldIP, newIP string) (bool, string) {
	if s.dns == nil {
		return false, "公网 IP 已改，但尚未配置 DNS 服务商，解析未变动"
	}
	switch err := s.dns.Sync(ctx, nil); {
	case errors.Is(err, dnsops.ErrNoProvider):
		return false, "公网 IP 已改，但尚未配置 DNS 服务商，解析未变动"
	case err != nil:
		return false, "公网 IP 已改（" + oldIP + " → " + newIP +
			"），但同步到 DNS 服务商失败：" + err.Error()
	default:
		return true, "公网 IP 已改（" + oldIP + " → " + newIP + "），解析已同步到服务商"
	}
}

// handleDeleteNode 删掉一个节点的记录。
//
// **必须先下线。** 一台还连着的机器手里有隧道证书，删掉记录之后它会重连、
// 会被 identify() 按证书认出来、然后在一张不存在的行上写心跳——
// 那是一个连着而看不见的幽灵。下线（ADR-0014）断隧道并拒绝重连，
// 所以它是这个操作真正的前提，不是一道礼节性的确认。
//
// **只删记录，不碰那台机器。** 上面的 Agent 与 Caddy 还在跑，
// 要真正撤掉得去那台机器上跑 `edge-node.sh uninstall`。
// 这句话必须出现在响应里——否则人会以为点了删除就干净了。
func (s *Server) handleDeleteNode(c *gin.Context) {
	nodeID := c.Param("id")
	setAuditTarget(c, nodeID)
	ctx := c.Request.Context()

	drained, err := s.store.IsNodeDrained(ctx, nodeID)
	if err != nil {
		s.log.Error("查下线状态失败", "node", nodeID, "err", err)
		Fail(c, CodeDownstream, "删除节点失败")
		return
	}
	if !drained {
		Fail(c, CodeStateConflict,
			"先「下线」这个节点再删除 —— 还连着的机器会带着隧道证书重连，"+
				"而它的记录已经没了，结果是一台连着却看不见的机器")
		return
	}

	if err := s.store.DeleteNode(ctx, nodeID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			Fail(c, CodeNotFound, "没有这个节点")
			return
		}
		s.log.Error("删除节点失败", "node", nodeID, "err", err)
		Fail(c, CodeDownstream, "删除节点失败")
		return
	}

	// 内存里那份观测状态也要丢掉，否则它会一直占着 CPU 序列和最近一次采样，
	// 而那个节点已经不存在了。
	if s.health != nil {
		s.health.Forget(nodeID)
	}

	OK(c, gin.H{
		"id": nodeID,
		"detail": "已删除记录。注意那台机器上的 Agent 与 Caddy 还在跑，" +
			"要真正撤掉在那台机器上执行：sudo ./edge-node.sh uninstall",
	})
}
