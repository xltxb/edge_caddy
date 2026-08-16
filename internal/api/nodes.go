package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

// Nodes 是 api 需要的节点通道能力。
type Nodes interface {
	Connected() []string
	Send(nodeID string, msg *edgev1.MasterMsg) error
	Probe(ctx context.Context, nodeID string, timeout time.Duration) (tunnel.ProbeReport, error)
}

// ProbeTimeout 是等待节点回报探活的上限。
//
// 取 5 秒：跨洲往返几百毫秒足够，再长就是节点侧卡住了，
// 而界面上转圈超过几秒人就会以为是页面挂了。
const ProbeTimeout = 5 * time.Second

// failNodeErr 把节点操作的错误映射成状态码。
//
// 「节点未连接」是 **404** 而不是 500：前者该等节点上线或去查那台机器，
// 后者该查主控日志或重试。混成一个的话，运维每次都得先翻日志才知道往哪看。
func (h *handler) failNodeErr(c *gin.Context, nodeID string, err error) {
	if errors.Is(err, tunnel.ErrNodeNotConnected) {
		fail(c, http.StatusNotFound, codeNotFound, "节点 "+nodeID+" 当前未连接")
		return
	}
	h.failErr(c, err, "节点操作失败")
}

func (h *handler) nodesReady(c *gin.Context) bool {
	if h.deps.Nodes == nil {
		fail(c, http.StatusServiceUnavailable, codeInternal, "节点通道未就绪")
		return false
	}
	return true
}

func (h *handler) connected(nodeID string) bool {
	for _, n := range h.deps.Nodes.Connected() {
		if n == nodeID {
			return true
		}
	}
	return false
}

// probeNode 探活。
func (h *handler) probeNode(c *gin.Context) {
	if !h.nodesReady(c) {
		return
	}
	id := c.Param("id")
	if !h.connected(id) {
		h.failNodeErr(c, id, tunnel.ErrNodeNotConnected)
		return
	}
	rep, err := h.deps.Nodes.Probe(c.Request.Context(), id, ProbeTimeout)
	if err != nil {
		if errors.Is(err, tunnel.ErrNodeNotConnected) {
			h.failNodeErr(c, id, err)
			return
		}
		// 「连着但不回话」是 **504**，不是 500 也不是 404：
		// 主控没坏（不是 500），节点也没断（不是 404），是那台机器上的
		// Agent 卡住了。原因原样带给用户——那正是他要看的东西。
		h.log.Warn("探活未收到回报", "node_id", id, "err", err)
		fail(c, http.StatusGatewayTimeout, codeInternal, err.Error())
		return
	}
	// Caddy 不可达**不影响**这次探活成功——隧道是通的，如实回报即可。
	ok(c, gin.H{
		"node_id":      id,
		"rtt_ms":       rep.RTT.Milliseconds(),
		"cfg_version":  rep.CfgVersion,
		"caddy_ok":     rep.CaddyOK,
		"caddy_detail": rep.CaddyDetail,
		"logs":         rep.Logs,
	})
}

// pushNode 把当前基线重推给单个节点。
func (h *handler) pushNode(c *gin.Context) {
	if !h.nodesReady(c) {
		return
	}
	id := c.Param("id")
	if !h.connected(id) {
		h.failNodeErr(c, id, tunnel.ErrNodeNotConnected)
		return
	}
	baseline, err := h.deps.Store.Baseline(c.Request.Context())
	if err != nil {
		h.failErr(c, err, "读取基线失败")
		return
	}
	if baseline == "" {
		// 尚无基线时重推等于推一份空配置——把节点上正在跑的服务全部清掉，
		// 一次「重推」变成一次全站中断。
		fail(c, http.StatusConflict, codeConflict,
			"尚未发布过配置，无法重推——那会把节点上正在跑的配置清空")
		return
	}
	if h.deps.Deploy == nil {
		fail(c, http.StatusServiceUnavailable, codeInternal, "下发功能未装配")
		return
	}
	res, err := h.deps.Deploy.Deploy(c.Request.Context(), operatorOf(c), nil)
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, codeBadInput, err.Error())
		return
	}
	h.log.Info("重推配置", "operator", operatorOf(c), "node_id", id, "cfg_version", res.CfgVersion)
	ok(c, gin.H{"node_id": id, "cfg_version": res.CfgVersion, "results": res.Rows})
}

type drainInput struct {
	Reason string `json:"reason"`
}

// drainNode 让节点退出服务。
func (h *handler) drainNode(c *gin.Context) {
	if !h.nodesReady(c) {
		return
	}
	id := c.Param("id")
	if !h.connected(id) {
		h.failNodeErr(c, id, tunnel.ErrNodeNotConnected)
		return
	}
	var in drainInput
	_ = c.ShouldBindJSON(&in) // 请求体可选

	if err := h.deps.Store.MarkNodeDown(c.Request.Context(), id); err != nil {
		h.failErr(c, err, "标记节点下线失败")
		return
	}
	h.log.Warn("节点被手动下线", "operator", operatorOf(c), "node_id", id, "reason", in.Reason)
	if h.deps.Hub != nil {
		h.deps.Hub.Broadcast(wsEvent("warn", id, "节点被手动下线"+reasonSuffix(in.Reason)))
	}
	ok(c, gin.H{"node_id": id})
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return "：" + reason
}
