package api

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// serveWeb 伺服控制台的静态文件，并给单页应用做 fallback。
//
// **这段代码此前不存在。** `EC_WEB_ROOT` 从第一天起就被读进配置，
// 而没有任何地方引用它——主控对 `/`、`/nodes`、`/assets/index-….js`
// 一律回 404。是前端 agent 在灰度打包时真起了一个主控发现的。
//
// 它长在两个包**唯一的接缝**上：后端出 master，前端出一堆静态文件，
// 而把它们接起来的就是这一个变量。
//
// （`scripts/unread.py` 没抓到它：那个扫描只看 DB 列和 proto 字段，
// **不看配置项**。已经补上了。）
func (s *Server) serveWeb(root string) gin.HandlerFunc {
	index := filepath.Join(root, "index.html")
	files := http.FileServer(http.Dir(root))

	return func(c *gin.Context) {
		p := c.Request.URL.Path

		// **API 路径永不 fallback。**
		//
		// 一个不存在的 API 路径回 index.html，前端会拿到一整页 HTML
		// 去 JSON.parse ——报出来的错跟真正的问题（路径写错了）
		// 毫无关系，而人会照着那个错去查解析代码。
		// `/api/` 已经盖住了 WebSocket（它的真实路径是 `/api/v1/ws`，
		// 契约 §2）。裸的 `/ws` 也一并挡住：它不是一个前端路由，
		// 回 index.html 只会让一个配错了路径的客户端拿到 HTML。
		if strings.HasPrefix(p, "/api/") || p == "/ws" {
			c.JSON(http.StatusNotFound,
				Envelope{Code: CodeOK, Data: nil, Msg: "端点不存在"})
			return
		}

		if root == "" {
			s.notDeployed(c)
			return
		}

		// 真实存在的文件直接给。`http.Dir` 自己会挡住 `..` 逃逸，
		// 手写路径拼接是这一类功能最常见的漏洞来源。
		if f, err := http.Dir(root).Open(path.Clean(p)); err == nil {
			st, serr := f.Stat()
			_ = f.Close()
			if serr == nil && !st.IsDir() {
				files.ServeHTTP(c.Writer, c.Request)
				return
			}
		}

		// **`/assets/` 下找不到就 404，不 fallback。**
		//
		// 判据跟上面的 `/api/` 完全一样：**那个路径下的东西只有一种消费者，
		// 而那个消费者不认识 HTML**。浏览器会拿一整页 HTML 当 JavaScript 执行，
		// 报 `Unexpected token '<'` —— 而真正的问题是那个文件不在，
		// 人会去看那个文件的语法，而它的语法完全正确。
		//
		// 灰度上这很容易发生：包传了一半、`index.html` 与 `assets/` 版本不匹配。
		//
		// **按目录判，不按扩展名判。** 「路径里有点就不 fallback」会误伤
		// 工作台的资源 key（`/workbench/route:api.example.com` 带点也带冒号），
		// 而那是人刷新页面时最常撞上的路径之一。
		if strings.HasPrefix(p, "/assets/") {
			c.Status(http.StatusNotFound)
			return
		}

		// 其余一律回 index.html：控制台是单页应用，`/nodes`、
		// `/workbench/global:tls` 这些路径在服务端不存在，由前端路由接管。
		if _, err := os.Stat(index); err != nil {
			s.notDeployed(c)
			return
		}
		c.File(index)
	}
}

// notDeployed 在前端产物不在时说清楚，而不是回一个空白页或 500。
//
// **「没部署前端」和「前端崩了」要分得开。** 主控可以只跑 API
// （灰度时先起后端是常见做法），那时打开根路径的人需要知道
// 缺的是什么、该怎么补——而不是对着一个 404 猜。
func (s *Server) notDeployed(c *gin.Context) {
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusNotFound,
		"控制台的静态文件不在。\n\n"+
			"主控从 EC_WEB_ROOT 找它们（当前：%q）。\n"+
			"把前端产物解包到那个目录，或者把 EC_WEB_ROOT 指过去。\n\n"+
			"API 不受影响，/api/v1/* 照常工作。\n", s.webRoot)
}
