package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/store"
)

const (
	ctxKeyPrincipal = "ec.principal"
	ctxKeyCode      = "ec.code"
	ctxKeyAction    = "ec.audit.action"
	ctxKeyTarget    = "ec.audit.target"
	ctxKeyOperator  = "ec.audit.operator"
	ctxKeyResult    = "ec.audit.result"
	ctxKeyDetail    = "ec.audit.detail"
)

// Principal 是这次请求背后的身份。两种：人（会话 Cookie）与 ops-bot（静态 Bearer）。
type Principal struct {
	Name string
	Kind string // human | bot
}

func principalOf(c *gin.Context) (Principal, bool) {
	v, ok := c.Get(ctxKeyPrincipal)
	if !ok {
		return Principal{}, false
	}
	p, ok := v.(Principal)
	return p, ok
}

// Recover 把 panic 变成 500 + 包裹体。
//
// msg 是固定的通用文案：panic 的内容里可能有连接串、SQL、请求体，
// 那些属于日志，不属于响应。
func Recover(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("handler panic",
					"path", c.FullPath(), "method", c.Request.Method,
					"panic", r, "stack", string(debug.Stack()))
				c.AbortWithStatusJSON(http.StatusInternalServerError,
					Envelope{Code: CodeOK, Data: nil, Msg: "服务内部错误"})
			}
		}()
		c.Next()
	}
}

// Auth 解析身份。公开路由（登录页、静态资源）不经过它。
//
// GET /auth/session 是个例外：它也要求身份，但 401 在那里是**正常结果**，
// 前端在那一处不跳转。这个区别在前端，后端一视同仁。
func Auth(st *store.Store, opsBotToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if tok := bearerToken(c.Request); tok != "" {
			// ops-bot 用静态 Bearer。常数时间比较：token 比对是认证边界，
			// 早退的比较会泄露前缀长度。
			if opsBotToken != "" && subtleEqual(tok, opsBotToken) {
				c.Set(ctxKeyPrincipal, Principal{Name: "ops-bot", Kind: "bot"})
				c.Next()
				return
			}
			Unauthorized(c)
			return
		}

		sid, err := c.Cookie(sessionCookieName)
		if err != nil || sid == "" {
			Unauthorized(c)
			return
		}
		username, err := st.SessionOwner(c.Request.Context(), sid)
		if err != nil {
			Unauthorized(c)
			return
		}
		c.Set(ctxKeyPrincipal, Principal{Name: username, Kind: "human"})
		c.Next()
	}
}

// audited 把一次写操作的审计动作声明在**路由上**而不是 handler 里。
//
// action 的取值是契约 §5 定死的表（「下发配置」而不是「推送配置」——
// 这些字符串会原样显示在审计页上，所以它们是契约的一部分，不是实现细节）。
// 声明在路由上的好处是：装配代码一眼能看出哪个写操作漏了审计。
func audited(action string, h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(ctxKeyAction, action)
		h(c)
	}
}

// setAuditTarget 由 handler 调用，补上动作的对象（域名、节点 id、cfg 版本…）。
func setAuditTarget(c *gin.Context, target string) { c.Set(ctxKeyTarget, target) }

// setAuditOperator 用于**公开路由**：登录发生在鉴权之前，中间件那时还没有 principal。
//
// 不补这一步的话，失败的登录会记成 operator="-"，而审计页对失败登录是单独提示的——
// 一条不说明「试的是哪个用户名」的告警，看到了也不知道该做什么。
func setAuditOperator(c *gin.Context, name string) { c.Set(ctxKeyOperator, name) }

// setAuditPartial 用于「部分成功」——下发是典型：5 个节点成功 1 个失败，
// 记成 ok 或 fail 都是撒谎。
func setAuditPartial(c *gin.Context, detail string) {
	c.Set(ctxKeyResult, "partial")
	c.Set(ctxKeyDetail, detail)
}

// Audit 在 handler 之后写审计行。只对声明了 action 的路由生效。
//
// 写审计失败不影响响应：审计是旁路，让它把一次成功的操作变成失败是本末倒置。
// 但它必须被记进日志，否则「审计静默丢行」这种事没人会发现。
func Audit(st *store.Store, log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		actionV, ok := c.Get(ctxKeyAction)
		if !ok {
			return
		}
		action, _ := actionV.(string)
		if action == "" {
			return
		}

		operator := ""
		if v, ok := c.Get(ctxKeyOperator); ok {
			operator, _ = v.(string)
		}
		if operator == "" {
			p, _ := principalOf(c)
			operator = p.Name
		}
		if operator == "" {
			operator = "-"
		}

		result := "ok"
		if v, ok := c.Get(ctxKeyResult); ok {
			result, _ = v.(string)
		} else if code, ok := c.Get(ctxKeyCode); ok {
			if n, _ := code.(int); n != CodeOK {
				result = "fail"
			}
		} else if c.Writer.Status() >= 400 {
			result = "fail"
		}

		target, _ := c.Get(ctxKeyTarget)
		targetStr, _ := target.(string)
		detail, _ := c.Get(ctxKeyDetail)
		detailStr, _ := detail.(string)

		rec := store.AuditRecord{
			Operator: operator,
			Action:   action,
			Target:   targetStr,
			SrcIP:    clientIP(c),
			Result:   result,
			Detail:   detailStr,
		}
		if err := st.InsertAudit(context.WithoutCancel(c.Request.Context()), rec); err != nil {
			log.Error("写审计失败", "action", action, "operator", operator, "err", err)
		}
	}
}

func clientIP(c *gin.Context) string {
	ip := c.ClientIP()
	if ip == "" {
		return ""
	}
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}
