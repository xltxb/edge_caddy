// Package api 是控制台的 HTTP 接口。
//
// 统一响应包裹 {code, data, msg}（前端文档 §6）：code 非 0 时 msg 是用户可读的
// 中文，前端直接进 toast，不再二次翻译——翻译层是错误信息失真的常见来源。
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xltxb/edge_caddy/internal/auth"
	"github.com/xltxb/edge_caddy/internal/deploy"
	"github.com/xltxb/edge_caddy/internal/enroll"
	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// 业务错误码。0 表示成功；其余按类别分段，便于前端按段处理。
const (
	codeOK           = 0
	codeBadInput     = 1001
	codeNotFound     = 1004
	codeConflict     = 1009
	codeUnauthorized = 1401
	codeInternal     = 1500
)

// Store 是 api 需要的存储能力。
type Store interface {
	ListNodes(ctx context.Context) ([]model.Node, error)
	Baseline(ctx context.Context) (string, error)
	ListRoutes(ctx context.Context) ([]model.Route, error)
	GetRoute(ctx context.Context, domain string) (model.Route, error)
	PutRoute(ctx context.Context, r model.Route) error
	DeleteRoute(ctx context.Context, domain string) error
	ListDeploys(ctx context.Context, limit int) ([]model.Deploy, map[int64][]model.DeployResult, error)
	ListDrafts(ctx context.Context) ([]model.Draft, error)
	PutDraft(ctx context.Context, key string, patch map[string]any, by string, now time.Time) error
	DeleteDrafts(ctx context.Context, keys []string) error
	AppendAudit(ctx context.Context, a model.AuditLog) error
	ListAudit(ctx context.Context, operator string, limit int) ([]model.AuditLog, error)
}

// Deployer 是 api 需要的下发能力。
type Deployer interface {
	// Deploy 只携带 resKeys 指定的资源；resKeys 为空表示全部。
	Deploy(ctx context.Context, operator string, resKeys []string) (deploy.Result, error)
	Preview(ctx context.Context, resKeys []string) (current, next string, err error)
	Rollback(ctx context.Context, cfgVersion, operator string) ([]string, error)
}

type Deps struct {
	Store  Store
	Auth   *auth.Manager
	Enroll *enroll.Enroller
	Deploy Deployer
	Hub    *ws.Hub
	Logger *slog.Logger
}

type handler struct {
	deps Deps
	log  *slog.Logger
}

// New 装配路由。
func New(d Deps) http.Handler {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	h := &handler{deps: d, log: log}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	v1 := r.Group("/api/v1")
	// 登录与登出不经**鉴权**中间件（它们正是用来获得会话的），但必须过
	// **审计**中间件：失败登录是排查爆破的第一手线索，漏了它等于放弃了这条线索。
	v1.Use(h.auditMiddleware())
	{
		v1.POST("/login", h.login)
		v1.POST("/logout", h.logout)
	}
	authed := r.Group("/api/v1")
	authed.Use(h.requireSession(), h.auditMiddleware())
	{
		authed.GET("/me", h.me)
		authed.GET("/nodes", h.listNodes)
		authed.POST("/nodes/token", h.issueNodeToken)

		authed.GET("/routes", h.listRoutes)
		authed.POST("/routes", h.createRoute)
		authed.PUT("/routes/:domain", h.updateRoute)
		authed.DELETE("/routes/:domain", h.deleteRoute)

		authed.POST("/config/preview", h.preview)
		authed.POST("/deploys", h.createDeploy)
		authed.GET("/deploys", h.listDeploys)
		authed.POST("/deploys/:cfg/rollback", h.rollbackDeploy)

		authed.GET("/drafts", h.listDrafts)
		authed.PUT("/drafts/:key", h.putDraft)
		authed.DELETE("/drafts", h.deleteDrafts)

		authed.GET("/audit", h.listAudit)

		authed.GET("/ws", h.serveWS)
	}
	return r
}

// ── 响应包裹 ──

func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"code": codeOK, "data": data, "msg": ""})
}

func fail(c *gin.Context, status, code int, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"code": code, "data": nil, "msg": msg})
}

func (h *handler) failErr(c *gin.Context, err error, msg string) {
	// 错误详情进日志，不进响应：内部错误的原文常带表名、路径、SQL 片段。
	h.log.Error(msg, "err", err, "path", c.Request.URL.Path)
	fail(c, http.StatusInternalServerError, codeInternal, msg)
}

// ── 鉴权 ──

const ctxUser = "edge.user"

// requireSession 校验会话。
//
// 尚未设置管理员口令时整个接口敞开（设计稿登录页写明的首次部署行为）。这是个
// 危险的默认值，因此 /me 会如实回报 auth_required=false，主控启动时也应显著告警——
// 一台刚起来的主控是敞开的，这件事不该只写在文档里。
func (h *handler) requireSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.deps.Auth == nil || !h.deps.Auth.Enabled(c.Request.Context()) {
			c.Set(ctxUser, "")
			c.Next()
			return
		}
		sid, err := c.Cookie(auth.CookieName)
		if err != nil {
			fail(c, http.StatusUnauthorized, codeUnauthorized, "未登录或登录已过期")
			return
		}
		user := h.deps.Auth.UserBySession(sid)
		if user == "" {
			fail(c, http.StatusUnauthorized, codeUnauthorized, "未登录或登录已过期")
			return
		}
		c.Set(ctxUser, user)
		c.Next()
	}
}

func operatorOf(c *gin.Context) string {
	if v, ok := c.Get(ctxUser); ok {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	return "anonymous"
}

type loginInput struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

func (h *handler) login(c *gin.Context) {
	var in loginInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, codeBadInput, "请求体不是合法 JSON")
		return
	}
	if h.deps.Auth == nil {
		fail(c, http.StatusInternalServerError, codeInternal, "鉴权未装配")
		return
	}

	sid, err := h.deps.Auth.Login(c.Request.Context(), in.User, in.Password)
	switch {
	case errors.Is(err, auth.ErrNoCredential):
		fail(c, http.StatusUnauthorized, codeUnauthorized, "尚未设置管理员口令，请用 EDGE_ADMIN_PASSWORD 初始化")
		return
	case errors.Is(err, auth.ErrBadCredential):
		markAudit(c, "fail")
		h.log.Warn("登录失败", "user", in.User, "src_ip", c.ClientIP())
		fail(c, http.StatusUnauthorized, codeUnauthorized, "用户名或口令不正确")
		return
	case err != nil:
		h.failErr(c, err, "登录失败")
		return
	}

	// Secure 只在 HTTPS 下设置：本地 http 调试时设了会导致 Cookie 根本存不下来。
	secure := c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https"
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(auth.CookieName, sid, int(auth.SessionTTL.Seconds()), "/", "", secure, true)
	// 登录成功时操作人来自本次登录结果，而不是中间件里的会话——
	// 中间件跑在登录之前，那时还没有会话。
	c.Set(ctxUser, auth.AdminUser)
	markAudit(c, "ok")
	ok(c, gin.H{"user": auth.AdminUser})
}

func (h *handler) logout(c *gin.Context) {
	if sid, err := c.Cookie(auth.CookieName); err == nil && h.deps.Auth != nil {
		h.deps.Auth.Logout(sid)
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(auth.CookieName, "", -1, "/", "", false, true)
	ok(c, nil)
}

func (h *handler) me(c *gin.Context) {
	required := h.deps.Auth != nil && h.deps.Auth.Enabled(c.Request.Context())
	ok(c, gin.H{
		"user": operatorOf(c),
		// 前端靠这个字段决定要不要跳登录页。它要是撒谎，用户会以为自己受保护着。
		"auth_required": required,
	})
}

// ── 节点 ──

func (h *handler) listNodes(c *gin.Context) {
	ctx := c.Request.Context()
	nodes, err := h.deps.Store.ListNodes(ctx)
	if err != nil {
		h.failErr(c, err, "读取节点列表失败")
		return
	}
	baseline, err := h.deps.Store.Baseline(ctx)
	if err != nil {
		h.failErr(c, err, "读取基线失败")
		return
	}

	out := make([]gin.H, 0, len(nodes))
	drifted := make([]string, 0)
	for _, n := range nodes {
		// 配置漂移 = 节点上报的版本 ≠ 当前基线（docs/adr/0002）。
		// 它只回答「这次下发到没到」，**不检查节点上的配置内容**。
		// 尚无基线时不判漂移：那时所有节点都还没收到过任何配置。
		isDrifted := baseline != "" && n.CfgVersion != baseline
		if isDrifted {
			drifted = append(drifted, n.ID)
		}
		out = append(out, gin.H{
			"id": n.ID, "city": n.City, "vendor": n.Vendor, "line": n.Line,
			"ip": n.PublicIP, "status": n.Status, "cfg": n.CfgVersion,
			"dns": n.DNSEnabled, "drifted": isDrifted,
			"last_hb": n.LastHB.UTC().Format(time.RFC3339),
		})
	}
	ok(c, gin.H{"nodes": out, "baseline": baseline, "drifted": drifted})
}

func (h *handler) issueNodeToken(c *gin.Context) {
	if h.deps.Enroll == nil {
		fail(c, http.StatusServiceUnavailable, codeInternal, "接入功能未装配")
		return
	}
	tok, exp, err := h.deps.Enroll.Issue(c.Request.Context(), enroll.TokenTTL)
	if err != nil {
		h.failErr(c, err, "签发接入 Token 失败")
		return
	}
	h.log.Info("签发接入 Token", "operator", operatorOf(c), "expires_at", exp)
	ok(c, gin.H{
		"token":      tok,
		"expires_at": exp.UTC().Format(time.RFC3339),
		"note":       "该 Token 一次性，用于换取隧道客户端证书；用过即失效。",
		// Token 走环境变量而不是命令行参数：命令行参数会出现在 ps 输出里，
		// 任何本机用户都能看到。部署脚本里也是同一条规矩（凭据写
		// EnvironmentFile 而非 ExecStart）。
		"install": "EDGE_ENROLL_TOKEN=" + tok +
			" edge-agent enroll --node-id <节点ID> --master " + c.Request.Host,
	})
}
