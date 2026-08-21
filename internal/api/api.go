package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/xltxb/edge_caddy/internal/alert"
	"github.com/xltxb/edge_caddy/internal/certs"
	"github.com/xltxb/edge_caddy/internal/health"
	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/tunnel"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/deploy"
	"github.com/xltxb/edge_caddy/internal/dnsops"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// Tunneler 是隧道在 HTTP 面这一层的最小面貌。
type Tunneler interface {
	OnlineNodes() []string
	Probe(ctx context.Context, nodeID string, timeout time.Duration) (tunnel.ProbeOutcome, error)
}

// Healther 是观测状态在 HTTP 面这一层的最小面貌。
type Healther interface {
	CPUSeries(nodeID string) []int
	Latest(nodeID string) (health.Sample, bool)
}

type Server struct {
	store        *store.Store
	log          *slog.Logger
	sessionTTL   time.Duration
	secureCookie bool

	tunnel           Tunneler
	health           Healther
	dns              *dnsops.Orchestrator
	alerts           *alert.Notifier
	certs            *certs.Manager
	sealer           *secret.Sealer
	deployer         *deploy.Scheduler
	masterAddr       string
	caPin            string
	opsBotConfigured bool
}

type Options struct {
	Store    *store.Store
	Hub      *ws.Hub
	Tunnel   Tunneler
	Health   Healther
	DNS      *dnsops.Orchestrator
	Alerts   *alert.Notifier
	Certs    *certs.Manager
	Sealer   *secret.Sealer
	Deployer *deploy.Scheduler
	Log      *slog.Logger

	// MasterAddr 与 CAPin 只用来拼安装命令。CAPin 让 Agent 首连时能确认
	// 对面就是主控，堵住 TOFU 那个洞（ADR-0009）。
	MasterAddr  string
	CAPin       string
	SessionTTL  time.Duration
	OpsBotToken string

	// SecureCookie 应当与「控制台跑在 TLS 上」一致。ADR-0013 下默认关闭：
	// 首版绑内网 HTTP，Secure Cookie 在 http:// 下不会被浏览器存下来。
	SecureCookie bool
}

// New 装配路由。契约见 docs/api-contract.md。
//
// 这里只装配 #17 骨架涉及的部分：会话与审计查询。其余端点随各自的 issue 落地，
// 未实现的路径**不注册**——注册一个返回空数据的桩会让前端以为它通了，
// 而那正是上一版「16 个 issue 每个都做了一点」的失败形态。
func New(o Options) *gin.Engine {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(Recover(o.Log))

	s := &Server{
		store:            o.Store,
		log:              o.Log,
		sessionTTL:       o.SessionTTL,
		secureCookie:     o.SecureCookie,
		tunnel:           o.Tunnel,
		health:           o.Health,
		dns:              o.DNS,
		alerts:           o.Alerts,
		certs:            o.Certs,
		sealer:           o.Sealer,
		deployer:         o.Deployer,
		masterAddr:       o.MasterAddr,
		caPin:            o.CAPin,
		opsBotConfigured: o.OpsBotToken != "",
	}

	v1 := r.Group("/api/v1")

	// 公开：登录本身不能要求已登录。
	v1.POST("/auth/login", Audit(o.Store, o.Log), audited("登录", s.handleLogin))

	authed := v1.Group("", Auth(o.Store, o.OpsBotToken), Audit(o.Store, o.Log))
	authed.POST("/auth/logout", audited("登出", s.handleLogout))
	authed.GET("/auth/session", s.handleSession)

	// WS 复用会话 Cookie，因此挂在 authed 组里：未登录直接 401，不升级（契约 §0.6）。
	//
	// **无条件注册。** 条件注册意味着装配漏了 Hub 时这个端点会静默 404，
	// 而 404 读起来是「没实现」而不是「配错了」——排查方向完全不同。
	authed.GET("/ws", func(c *gin.Context) {
		if o.Hub == nil {
			Fail(c, CodeStateConflict, "实时通道未装配")
			return
		}
		ws.Handler(o.Hub, o.Log)(c.Writer, c.Request)
	})

	authed.GET("/overview", s.handleOverview)
	authed.GET("/audit", s.handleAudit)

	authed.GET("/nodes", s.handleListNodes)
	authed.POST("/nodes/:id/push", audited("重推配置", s.handleNodePush))
	authed.POST("/nodes/:id/dns", s.handleNodeDNS)
	authed.POST("/nodes/:id/probe", s.handleNodeProbe)
	authed.POST("/nodes/:id/drain", audited("下线节点", s.handleNodeDrain))

	authed.GET("/certs", s.handleListCerts)
	authed.POST("/certs/:domain/renew", audited("续期证书", s.handleRenewCert))
	authed.POST("/certs/renew-check", audited("续期证书", s.handleRenewCheck))

	authed.GET("/dns/weights", s.handleGetDNSWeights)
	authed.PUT("/dns/weights", audited("调整解析权重", s.handlePutDNSWeights))

	authed.GET("/settings", s.handleGetSettings)
	authed.PUT("/settings", audited("修改系统设置", s.handlePutSettings))
	authed.GET("/alerts", s.handleGetAlerts)
	authed.PUT("/alerts", audited("修改告警设置", s.handlePutAlerts))
	authed.POST("/alerts/test", audited("发送告警测试", s.handleTestAlert))
	authed.POST("/nodes/token", audited("签发接入Token", s.handleIssueToken))

	authed.GET("/routes", s.handleListRoutes)
	authed.POST("/routes", audited("新建路由", s.handleCreateRoute))
	authed.PUT("/routes/:domain", audited("修改路由", s.handleUpdateRoute))
	authed.DELETE("/routes/:domain", audited("删除路由", s.handleDeleteRoute))

	authed.GET("/rules", s.handleListRules)
	authed.PUT("/rules/:id", audited("修改访问规则", s.handleUpsertRule))

	authed.GET("/policies/:id", s.handleGetPolicy)
	authed.PUT("/policies/:id", audited("修改全局策略", s.handlePutPolicy))

	authed.GET("/drafts", s.handleListDrafts)
	authed.PUT("/drafts/:key", audited("修改草稿", s.handlePutDraft))
	authed.DELETE("/drafts", audited("放弃草稿", s.handleDeleteDrafts))

	// 预览是只读的 dry-run，不写审计——它不改变任何东西。
	authed.POST("/deploys/preview", s.handlePreview)
	authed.POST("/deploys", audited("下发配置", s.handleDeploy))
	authed.GET("/deploys", s.handleListDeploys)
	authed.GET("/deploys/:id", s.handleGetDeploy)
	authed.POST("/deploys/:id/rollback", audited("回滚配置", s.handleRollback))

	// 端点不存在用 HTTP 404，而不是 CodeNotFound——后者表示**资源**不存在。
	// 混在一起前端就分不清「路由写错了」和「这条路由被别人删了」。
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, Envelope{Code: CodeOK, Data: nil, Msg: "端点不存在"})
	})
	return r
}
