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
	Disconnect(nodeID string) bool
	Drain(ctx context.Context, nodeID string, timeout time.Duration) (tunnel.DrainOutcome, error)
	// HTTPHandler 是隧道在 443 上的入口（见 internal/tunnel/wstransport.go）。
	HTTPHandler() http.HandlerFunc
}

// Healther 是观测状态在 HTTP 面这一层的最小面貌。
type Healther interface {
	CPUSeries(nodeID string) []int
	Latest(nodeID string) (health.Sample, bool)
	// Forget 丢掉一个节点的内存观测状态。删除节点时要调 ——
	// 不调的话那份 CPU 序列会一直占着，而节点已经不存在了。
	Forget(nodeID string)
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
	webRoot          string
	version          string
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
	MasterAddr string
	CAPin      string
	// Version 是这个主控二进制的构建标记（scripts/build.sh 用 -ldflags 注进去）。
	// 经 GET /overview 回给控制台 —— 见那里的注释：
	// **「修得不对」和「根本没部署」产生的观测一模一样。**
	Version     string
	SessionTTL  time.Duration
	OpsBotToken string
	// WebRoot 是控制台静态文件所在的目录（EC_WEB_ROOT）。
	//
	// **这是后端包与前端包唯一的接缝**：后端出一个 master，前端出一堆静态
	// 文件，把它们接起来的就是这一个值。留空则只跑 API，根路径会说清
	// 缺的是什么——而不是回一个让人去猜的 404。
	WebRoot string

	// SecureCookie 应当与「控制台跑在 TLS 上」一致。ADR-0013 下默认关闭：
	// 首版绑内网 HTTP，Secure Cookie 在 http:// 下不会被浏览器存下来。
	//
	// 它此前由 MTLSEnabled 决定，而那是个错误的耦合——前置 nginx 终止 TLS 时
	// 主控看到的是 HTTP 而浏览器走的是 HTTPS，那时该开它，跟 mTLS 无关。
	SecureCookie bool

	// TrustedProxies 是可信反代的地址。**空 = 谁也不信**，来源 IP 一律取
	// 连接的对端地址。
	//
	// gin 的默认是信任所有代理，于是任何能访问控制台的人发一个
	// X-Forwarded-For 就能伪造审计日志里的来源 IP——而审计是 ADR-0013
	// 准入模型的三分之一。这里显式收紧。
	TrustedProxies []string
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

	// **默认谁也不信。**
	//
	// gin 不调这个方法时信任所有代理，于是任何能访问控制台的人发一个
	// `X-Forwarded-For: 1.2.3.4` 就能让审计日志记下那个 IP。
	// 实测过：不配时 ClientIP 返回 1.2.3.4，配 SetTrustedProxies(nil)
	// 返回真实对端地址。
	//
	// **而审计是 ADR-0013 准入模型的三分之一**（只绑内网 + Cookie + 全写审计）
	// ——一个可以被访问者伪造的来源 IP，等于那三分之一在追责时不作数。
	//
	// 前置反代时把它的地址填进 EC_TRUSTED_PROXIES，那时 XFF 才被采信。
	if err := r.SetTrustedProxies(o.TrustedProxies); err != nil {
		o.Log.Error("可信代理配置无效，退回「谁也不信」", "err", err)
		_ = r.SetTrustedProxies(nil)
	}

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
		webRoot:          o.WebRoot,
		version:          o.Version,
	}

	v1 := r.Group("/api/v1")

	// 公开：登录本身不能要求已登录。
	v1.POST("/auth/login", Audit(o.Store, o.Log), audited("登录", s.handleLogin))

	// **隧道挂在鉴权组外面，这是有意的，不是漏了。**
	//
	// 调用方是 Agent，它没有控制台会话，也不该有——它的身份是内部 CA 签发的
	// 客户端证书，而那次握手发生在**这条 WebSocket 里面**（ADR-0009）。
	// 在这里加会话检查等于要求节点先登录控制台，那是另一套身份。
	//
	// 所以这个端点「未鉴权」的准确说法是：**认证在里层，不在这里**。
	// 升级成功不代表任何权限——里层握不出证书的连接，
	// 在 identify() 那一步照样被拒。
	//
	// 它存在的理由：中间设施（CDN、企业代理）只转发 80/443，
	// 隧道那个独立端口的包根本到不了主控。灰度上撞到过，
	// 症状是节点装完一切正常、**永远不出现在控制台里**。
	// **无条件注册**，跟下面那个 /ws 一样，理由也一样：条件注册意味着
	// 装配漏了隧道时这个端点会静默 404，而 404 读起来是「主控版本太老、
	// 没有这个端点」——节点那一侧会去查版本，而问题在装配。
	//
	// （这条注释是补上来的：我在下面 30 行写着这个理由，然后在这里
	// 写了 `if o.Tunnel != nil`。**知道和用上是两回事。**）
	v1.GET("/tunnel", func(c *gin.Context) {
		if o.Tunnel == nil {
			Fail(c, CodeStateConflict, "隧道未装配")
			return
		}
		o.Tunnel.HTTPHandler()(c.Writer, c.Request)
	})

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
	authed.GET("/nodes/:id/logs", s.handleNodeLogs)
	authed.POST("/nodes/:id/push", audited("重推配置", s.handleNodePush))
	authed.POST("/nodes/:id/dns", s.handleNodeDNS)
	authed.POST("/nodes/:id/probe", s.handleNodeProbe)
	authed.POST("/nodes/:id/drain", audited("下线节点", s.handleNodeDrain))
	authed.POST("/nodes/:id/rejoin", audited("重新上线", s.handleNodeRejoin))
	authed.PUT("/nodes/:id", audited("修改节点", s.handleUpdateNode))
	authed.DELETE("/nodes/:id", audited("删除节点", s.handleDeleteNode))

	authed.GET("/certs", s.handleListCerts)
	authed.PUT("/certs/:domain", audited("导入证书", s.handleImportCert))
	authed.DELETE("/certs/:domain", audited("删除证书", s.handleDeleteCert))

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
	authed.DELETE("/rules/:id", audited("删除访问规则", s.handleDeleteRule))

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

	// 没匹配到任何 API 路由的请求交给控制台：静态文件、或者单页应用的
	// fallback。API 路径永不 fallback（见 serveWeb）——
	// 端点不存在仍然是 HTTP 404 而不是 CodeNotFound，后者表示**资源**不存在，
	// 混在一起前端就分不清「路由写错了」和「这条路由被别人删了」。
	r.NoRoute(s.serveWeb(o.WebRoot))
	return r
}
