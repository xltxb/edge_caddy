package api

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
)

// bindStrict 解析请求体，**拒绝任何契约里没有的字段**。
//
// gin 的 ShouldBindJSON 静默忽略未知字段，于是一个写错的 key 会得到
// `code: 0`——**请求成功了，而什么也没存进去**。
//
// 前端 agent 就是这么撞上的：他发的是顶层 `dns_credential`，
// 而后端要的是 `dns_provider.credential`。返回 `code: 0`、界面提示
// 「设置已保存」，而 `configured` 一直是 false。
//
// **这是「没生效」那一族里最坏的一种：成功的假象。**
// 报错会让人再试，假象让人走开——他会去查别的地方，
// 因为「保存那一步明明成功了」。
//
// 代价是多一条错误路径：客户端多发一个字段会被拒。而这是内部接口，
// 契约就是它的全部——发一个契约里没有的字段本身就是错的，
// 早说比晚说好。
func bindStrict(c *gin.Context, dst any) error {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("读取请求体失败")
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		// **把「未知字段」翻成人话。** 标准库给的是
		// `json: unknown field "dns_credential"`，那句话已经点出了问题，
		// 但它读起来像内部错误。
		if f, ok := unknownField(err); ok {
			return fmt.Errorf("请求里有契约没有的字段 %q —— "+
				"多半是写错了 key，它不会被保存", f)
		}
		return fmt.Errorf("请求格式错误：%w", err)
	}
	return nil
}

func unknownField(err error) (string, bool) {
	const marker = "unknown field "
	s := err.Error()
	i := strings.Index(s, marker)
	if i < 0 {
		return "", false
	}
	return strings.Trim(s[i+len(marker):], `"`), true
}
