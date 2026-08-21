package api

import (
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"
)

const sessionCookieName = "ec_session"

func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type principalResp struct {
	Username string `json:"username"`
	Kind     string `json:"kind"`
}

func (s *Server) handleLogin(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, CodeBadParam, "请求格式错误")
		return
	}
	// 记的是**被尝试的**用户名，成功失败都记。失败时它正是审计页要提示的东西。
	setAuditOperator(c, req.Username)

	ctx := c.Request.Context()
	if !s.store.VerifyPassword(ctx, req.Username, req.Password) {
		// 不区分「用户名不存在」与「口令错误」。区分了就等于提供了一个
		// 用户名枚举接口，而失败的登录尝试在审计页上是单独提示的，
		// 攻击者的每一次试探都会留下痕迹。
		Fail(c, CodeBadParam, "用户名或密码错误")
		return
	}

	sid, err := s.store.CreateSession(ctx, req.Username, clientIP(c), s.sessionTTL)
	if err != nil {
		s.log.Error("创建会话失败", "err", err)
		Fail(c, CodeDownstream, "登录失败，请重试")
		return
	}

	// Secure 跟随 mTLS 开关：ADR-0013 下首版跑在内网 HTTP 上，
	// 硬写 Secure=true 会让 Cookie 在 http:// 下根本不被存下来，
	// 现象是「登录成功但立刻又跳回登录页」。
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(sessionCookieName, sid, int(s.sessionTTL.Seconds()), "/", "", s.secureCookie, true)

	OK(c, principalResp{Username: req.Username, Kind: "human"})
}

func (s *Server) handleLogout(c *gin.Context) {
	if sid, err := c.Cookie(sessionCookieName); err == nil && sid != "" {
		if err := s.store.DeleteSession(c.Request.Context(), sid); err != nil {
			s.log.Error("删除会话失败", "err", err)
		}
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(sessionCookieName, "", -1, "/", "", s.secureCookie, true)
	OK(c, nil)
}

// handleSession 让前端在启动时决定要不要跳登录。
//
// 后端文档 §4 没有这个端点，是前端 session store 的实际需要。
// 未登录时它返回 401——那是**正常结果**，前端在这一处不跳转，
// 只把 session 标记为未登录（api-contract §1）。
func (s *Server) handleSession(c *gin.Context) {
	p, ok := principalOf(c)
	if !ok {
		Unauthorized(c)
		return
	}
	OK(c, principalResp{Username: p.Name, Kind: p.Kind})
}
