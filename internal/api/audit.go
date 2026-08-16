package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xltxb/edge_caddy/internal/model"
)

// ctxAuditResult 让处理器覆盖审计结果（如登录失败时标记 fail）。
const ctxAuditResult = "edge.audit_result"

// markAudit 由处理器调用，显式指定这次操作的审计结果。
//
// 登录需要它：登录失败返回 401，而 401 在别处可能只是「没带 Cookie」，
// 不该一律算作一次失败的操作。
func markAudit(c *gin.Context, result string) {
	c.Set(ctxAuditResult, result)
}

// auditMiddleware 记录写操作。
//
// **只记写操作**：把 GET 也记下来会让流水被巡检刷满——每 3 秒一次的节点轮询
// 一天能产生几万条，真正重要的那几条写操作就淹没了。
//
// 审计在处理器**之后**写，因为要知道结果；但审计失败**绝不能**影响主操作：
// 反过来会让一次审计抖动升级成一次业务故障。写不进去时记错误日志，
// 那是唯一还能被发现的地方。
func (h *handler) auditMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet {
			c.Next()
			return
		}
		c.Next()

		result := "ok"
		if v, ok := c.Get(ctxAuditResult); ok {
			if s, _ := v.(string); s != "" {
				result = s
			}
		} else if c.Writer.Status() >= 400 {
			// 失败的写操作也要留痕：只记成功的话，「有人反复尝试改某条配置
			// 但一直被拒」这件事就完全看不见。
			result = "fail"
		}

		entry := model.AuditLog{
			Operator: operatorOf(c),
			Action:   c.Request.Method + " " + routePattern(c),
			Target:   targetOf(c),
			SrcIP:    c.ClientIP(),
			Result:   result,
			At:       time.Now(),
		}
		// 请求体一律不记：登录的请求体里就是明文口令，设置接口里是各种凭据。
		// 「先记下来以后再脱敏」是凭据泄漏最常见的来源。
		if err := h.deps.Store.AppendAudit(c.Request.Context(), entry); err != nil {
			h.log.Error("写入审计失败", "err", err, "action", entry.Action)
		}
	}
}

// routePattern 返回不含具体参数值的路由模板，让同类操作能被聚合统计。
func routePattern(c *gin.Context) string {
	p := c.FullPath()
	if p == "" {
		p = c.Request.URL.Path
	}
	const prefix = "/api/v1"
	if len(p) > len(prefix) && p[:len(prefix)] == prefix {
		return p[len(prefix):]
	}
	return p
}

// targetOf 取出被操作的对象（路径参数），便于按对象追溯。
func targetOf(c *gin.Context) string {
	for _, key := range []string{"domain", "id", "key", "cfg"} {
		if v := c.Param(key); v != "" {
			return v
		}
	}
	return ""
}

func (h *handler) listAudit(c *gin.Context) {
	logs, err := h.deps.Store.ListAudit(c.Request.Context(), c.Query("operator"), 200)
	if err != nil {
		h.failErr(c, err, "读取审计日志失败")
		return
	}
	ok(c, gin.H{"logs": logs})
}
