package api

import (
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/gin-gonic/gin"

	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/store"
)

// 域名与 host:port 的格式校验（设计稿新建向导里的同一套规则）。
var (
	domainRe   = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$`)
	upstreamRe = regexp.MustCompile(`^[\w.-]+:\d{1,5}$`)
)

type routeInput struct {
	Domain    string   `json:"domain"`
	Upstream  string   `json:"upstream"`
	Block     string   `json:"block"`
	MTLS      bool     `json:"mtls"`
	Compress  *bool    `json:"compress"`
	BodyMax   string   `json:"body_max"`
	Whitelist []string `json:"wl"`
}

// validate 在入库前拦下非法值。
//
// 必须在这里拦，不能等到下发：非法值一旦入库，**每一次**下发都会失败，而错误
// 出现在与那次配置操作完全无关的时刻——排查时很难联想到是几天前某条路由填错了。
func (in *routeInput) validate() error {
	in.Domain = strings.TrimSpace(in.Domain)
	in.Upstream = strings.TrimSpace(in.Upstream)
	if !domainRe.MatchString(in.Domain) {
		return errors.New("域名格式不正确")
	}
	if !upstreamRe.MatchString(in.Upstream) {
		return errors.New("回源地址应形如 10.8.0.2:8080")
	}
	if in.Block == "" {
		in.Block = string(model.BlockAbort)
	}
	if !model.BlockAction(in.Block).Valid() {
		return errors.New("非白名单处置方式只能是 abort / 403 / 404")
	}
	if in.BodyMax == "" {
		in.BodyMax = "5MB"
	}
	if _, err := humanize.ParseBytes(in.BodyMax); err != nil {
		return errors.New("请求体上限不是合法的大小，例如 5MB / 64MiB")
	}
	for _, s := range in.Whitelist {
		if t := strings.TrimSpace(s); t != "" && !validIPOrCIDR(t) {
			return errors.New(t + " 不是合法的 IP 或 CIDR")
		}
	}
	return nil
}

func validIPOrCIDR(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

func (in routeInput) toModel() model.Route {
	compress := true
	if in.Compress != nil {
		compress = *in.Compress
	}
	wl := make([]string, 0, len(in.Whitelist))
	for _, s := range in.Whitelist {
		if t := strings.TrimSpace(s); t != "" {
			wl = append(wl, t)
		}
	}
	return model.Route{
		Domain: in.Domain, Upstream: in.Upstream, Block: model.BlockAction(in.Block),
		MTLS: in.MTLS, Compress: compress, BodyMax: in.BodyMax, Whitelist: wl,
	}
}

func (h *handler) listRoutes(c *gin.Context) {
	rs, err := h.deps.Store.ListRoutes(c.Request.Context())
	if err != nil {
		h.failErr(c, err, "读取路由列表失败")
		return
	}
	ok(c, gin.H{"routes": rs})
}

func (h *handler) createRoute(c *gin.Context) {
	var in routeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, codeBadInput, "请求体不是合法 JSON")
		return
	}
	if err := in.validate(); err != nil {
		fail(c, http.StatusBadRequest, codeBadInput, err.Error())
		return
	}
	ctx := c.Request.Context()
	if _, err := h.deps.Store.GetRoute(ctx, in.Domain); err == nil {
		fail(c, http.StatusConflict, codeConflict, "域名 "+in.Domain+" 已存在")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		h.failErr(c, err, "检查域名是否重复失败")
		return
	}
	if err := h.deps.Store.PutRoute(ctx, in.toModel()); err != nil {
		h.failErr(c, err, "写入路由失败")
		return
	}
	h.log.Info("创建路由", "operator", operatorOf(c), "domain", in.Domain)
	ok(c, in.toModel())
}

func (h *handler) updateRoute(c *gin.Context) {
	domain := c.Param("domain")
	var in routeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, codeBadInput, "请求体不是合法 JSON")
		return
	}
	in.Domain = domain
	if err := in.validate(); err != nil {
		fail(c, http.StatusBadRequest, codeBadInput, err.Error())
		return
	}
	ctx := c.Request.Context()
	cur, err := h.deps.Store.GetRoute(ctx, domain)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, codeNotFound, "路由 "+domain+" 不存在")
		return
	}
	if err != nil {
		h.failErr(c, err, "读取路由失败")
		return
	}
	next := in.toModel()
	next.Version = cur.Version // 版本由下发推进，不由编辑推进
	if err := h.deps.Store.PutRoute(ctx, next); err != nil {
		h.failErr(c, err, "写入路由失败")
		return
	}
	h.log.Info("更新路由", "operator", operatorOf(c), "domain", domain)
	ok(c, next)
}

func (h *handler) deleteRoute(c *gin.Context) {
	domain := c.Param("domain")
	err := h.deps.Store.DeleteRoute(c.Request.Context(), domain)
	if errors.Is(err, store.ErrNotFound) {
		// 静默成功会让「删错了域名」看起来像删对了
		fail(c, http.StatusNotFound, codeNotFound, "路由 "+domain+" 不存在")
		return
	}
	if err != nil {
		h.failErr(c, err, "删除路由失败")
		return
	}
	h.log.Info("删除路由", "operator", operatorOf(c), "domain", domain)
	ok(c, nil)
}

// ── 下发 ──

type deployInput struct {
	// ResKeys 是本次勾选下发的资源键。为空表示全部。
	ResKeys []string `json:"res_keys"`
}

func (h *handler) createDeploy(c *gin.Context) {
	if h.deps.Deploy == nil {
		fail(c, http.StatusServiceUnavailable, codeInternal, "下发功能未装配")
		return
	}
	var in deployInput
	_ = c.ShouldBindJSON(&in) // 请求体可选

	res, err := h.deps.Deploy.Deploy(c.Request.Context(), operatorOf(c), in.ResKeys)
	if err != nil {
		// 渲染失败与「没有在线节点」都是可预期的拒绝，不是服务端故障，
		// 因此用 422 而不是 500——调用方的处理完全不同。
		fail(c, http.StatusUnprocessableEntity, codeBadInput, err.Error())
		return
	}
	h.log.Info("下发完成", "operator", operatorOf(c), "cfg_version", res.CfgVersion, "nodes", len(res.Rows))
	ok(c, gin.H{"deploy_id": res.DeployID, "cfg_version": res.CfgVersion, "results": res.Rows})
}

// listOverview 返回集群 KPI。
//
// 漂移口径与节点列表**共用同一个判定**（driftedOf），不能两处各算各的——
// 两处算法不同的表现是「总览说 2 个漂移、节点页只标出 1 个」，
// 而没人分得清哪个对。
func (h *handler) listOverview(c *gin.Context) {
	ctx := c.Request.Context()
	nodes, err := h.deps.Store.ListNodes(ctx)
	if err != nil {
		h.failErr(c, err, "读取节点失败")
		return
	}
	baseline, err := h.deps.Store.Baseline(ctx)
	if err != nil {
		h.failErr(c, err, "读取基线失败")
		return
	}
	routes, err := h.deps.Store.ListRoutes(ctx)
	if err != nil {
		h.failErr(c, err, "读取路由失败")
		return
	}

	online, drifted, conns := 0, 0, 0
	for _, n := range nodes {
		if n.Status != "down" {
			online++
		}
		if driftedOf(n, baseline) {
			drifted++
		}
	}
	ok(c, gin.H{
		"nodes_total": len(nodes), "nodes_online": online,
		"drifted": drifted, "conns": conns,
		"routes": len(routes), "baseline": baseline,
	})
}

// preview 返回下发前后的两份权威渲染，供确认弹层展示真实 diff（docs/adr/0007）。
func (h *handler) preview(c *gin.Context) {
	if h.deps.Deploy == nil {
		fail(c, http.StatusServiceUnavailable, codeInternal, "下发功能未装配")
		return
	}
	var in deployInput
	_ = c.ShouldBindJSON(&in)

	cur, next, err := h.deps.Deploy.Preview(c.Request.Context(), in.ResKeys)
	if err != nil {
		// 渲染失败是可预期的拒绝（配置本身有问题），不是服务端故障
		fail(c, http.StatusUnprocessableEntity, codeBadInput, err.Error())
		return
	}
	ok(c, gin.H{"current": cur, "next": next})
}

// rollbackDeploy 把某版本写回草稿。**不直接推送**（PRD §6.3）——
// 回滚往往发生在出事的时候，正是最需要有人看一眼 diff 的时刻。
func (h *handler) rollbackDeploy(c *gin.Context) {
	if h.deps.Deploy == nil {
		fail(c, http.StatusServiceUnavailable, codeInternal, "下发功能未装配")
		return
	}
	keys, err := h.deps.Deploy.Rollback(c.Request.Context(), c.Param("cfg"), operatorOf(c))
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, codeBadInput, err.Error())
		return
	}
	h.log.Info("回滚写回草稿", "operator", operatorOf(c), "cfg_version", c.Param("cfg"), "resources", len(keys))
	ok(c, gin.H{"res_keys": keys, "note": "已写回草稿，请在工作台检查 diff 后推送"})
}

func (h *handler) listDeploys(c *gin.Context) {
	ds, results, err := h.deps.Store.ListDeploys(c.Request.Context(), 50)
	if err != nil {
		h.failErr(c, err, "读取下发记录失败")
		return
	}
	baseline, err := h.deps.Store.Baseline(c.Request.Context())
	if err != nil {
		h.failErr(c, err, "读取基线失败")
		return
	}
	out := make([]gin.H, 0, len(ds))
	for _, d := range ds {
		rows := results[d.ID]
		if rows == nil {
			rows = []model.DeployResult{}
		}
		out = append(out, gin.H{
			"id": d.ID, "cfg_version": d.CfgVersion, "operator": d.Operator,
			"res_keys": d.ResKeys, "ok_count": d.OKCount, "fail_count": d.FailCount,
			"created_at": d.CreatedAt, "results": rows,
			// 当前基线不提供「回滚到自己」——那是个空操作
			"is_baseline": d.CfgVersion == baseline,
		})
	}
	ok(c, gin.H{"deploys": out, "baseline": baseline})
}

// driftedOf 判定配置漂移：节点上报的版本 ≠ 当前基线（docs/adr/0002）。
//
// 只比版本号，**不检查节点上的配置内容**。尚无基线时不判漂移——那时所有节点
// 都还没收到过任何配置，全标成漂移只会是噪声。
func driftedOf(n model.Node, baseline string) bool {
	return baseline != "" && n.CfgVersion != baseline
}
