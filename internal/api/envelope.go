// Package api 是主控的 HTTP 面。契约见 docs/api-contract.md。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Envelope 是所有响应的形状，错误也不例外（api-contract §0.1）。
type Envelope struct {
	Code int    `json:"code"`
	Data any    `json:"data"`
	Msg  string `json:"msg"`
}

// 业务错误码（api-contract §0.3）。
//
// 它们与 HTTP 状态码分工明确，不重复表达同一件事：会话用 401、权限用 403、
// 端点不存在用 404、未捕获异常用 500，其余业务失败一律 HTTP 200 + 非零 Code。
//
// 特别注意 CodeNotFound：**资源**不存在用它，不用 HTTP 404。
// 404 只表示「这个 URL 后端没实现」；混在一起前端就分不清
// 「路由写错了」和「这条路由被别人删了」。
const (
	CodeOK              = 0
	CodeBadParam        = 1001
	CodeValidation      = 1002
	CodeNotFound        = 1003
	CodeConflict        = 1004
	CodeStateConflict   = 2001
	CodeDownstream      = 3001
	CodeNodeUnreachable = 3002
)

// FieldError 把校验失败定位到具体输入框。
// 一句 msg 不够：工作台要让出错的那个输入框转红，落不到字段上就做不到
// （api-contract §0.3）。
type FieldError struct {
	ResKey string `json:"res_key"`
	Field  string `json:"field"` // 点号路径，数组下标用 [n]，例 spec.ips[2]
	Reason string `json:"reason"`
}

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{Code: CodeOK, Data: data, Msg: ""})
}

// Fail 是业务失败：HTTP 仍然是 200，靠 Code 表达。
func Fail(c *gin.Context, code int, msg string) {
	c.Set(ctxKeyCode, code)
	c.JSON(http.StatusOK, Envelope{Code: code, Data: nil, Msg: msg})
}

// FailValidation 是 CodeValidation 的专用出口。它是唯一一个 Data 不为 null
// 的失败响应——前端需要 errors 才能把错误落到字段上。
func FailValidation(c *gin.Context, msg string, errs []FieldError) {
	c.Set(ctxKeyCode, CodeValidation)
	c.JSON(http.StatusOK, Envelope{
		Code: CodeValidation,
		Data: gin.H{"errors": errs},
		Msg:  msg,
	})
}

// Unauthorized 走 HTTP 401。这是前端 http.ts 里唯一需要特判的码——
// 收到就跳登录页。因此除了「确实未登录」之外，任何情况都不要用它。
func Unauthorized(c *gin.Context) {
	c.Set(ctxKeyCode, CodeOK) // 未登录不是业务失败，不该记成审计里的 fail
	c.AbortWithStatusJSON(http.StatusUnauthorized,
		Envelope{Code: CodeOK, Data: nil, Msg: "未登录或会话已过期"})
}

func Forbidden(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusForbidden,
		Envelope{Code: CodeOK, Data: nil, Msg: msg})
}
