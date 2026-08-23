package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 造一份与前端产物同形的目录：根绝对路径的资源 + index.html。
func webRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", `<!doctype html><script src="/assets/index-abc.js"></script>`)
	write(filepath.Join("assets", "index-abc.js"), "console.log(1)")
	write(filepath.Join("assets", "index-abc.css"), "body{}")
	return dir
}

// **主控要伺服控制台。**
//
// 这段代码此前不存在：EC_WEB_ROOT 从第一天起就被读进配置，而没有任何地方
// 引用它——`/`、`/nodes`、`/assets/index-….js` 一律 404。
// 是前端 agent 在灰度打包时真起了一个主控发现的。
func TestServesConsoleAndFallsBackForSPARoutes(t *testing.T) {
	r, _ := newServerWithWeb(t, webRoot(t))

	// 根路径给 index.html。
	if code, body := get(r, "/"); code != 200 || !strings.Contains(body, "<!doctype html>") {
		t.Fatalf("/ = %d %q", code, body)
	}

	// 真实存在的资源直接给，MIME 由标准表决定。
	code, body := get(r, "/assets/index-abc.js")
	if code != 200 || body != "console.log(1)" {
		t.Fatalf("/assets/index-abc.js = %d %q", code, body)
	}

	// **单页路由要 fallback 到 index.html。**
	// 前端 agent 实测过：不做 fallback 的话 /nodes、/certs 全是 404，
	// 而那是人刷新页面时最常撞上的。
	for _, p := range []string{"/nodes", "/certs", "/workbench"} {
		if code, body := get(r, p); code != 200 || !strings.Contains(body, "<!doctype html>") {
			t.Errorf("%s 应当 fallback 到 index.html，实际 %d %q", p, code, body)
		}
	}

	// **带冒号的路径**。前端的工作台路由长这样，而各家路由器对冒号的
	// 处理不同——他专门提醒了这一条。
	if code, body := get(r, "/workbench/global:tls"); code != 200 ||
		!strings.Contains(body, "<!doctype html>") {
		t.Errorf("带冒号的路径也要 fallback，实际 %d %q", code, body)
	}
}

// **API 路径永不 fallback。**
//
// 一个不存在的 API 路径回 index.html，前端会拿到一整页 HTML 去 JSON.parse
// ——报出来的错跟真正的问题（路径写错了）毫无关系，而人会照着那个错
// 去查解析代码。
func TestAPIPathsNeverFallBackToIndex(t *testing.T) {
	r, _ := newServerWithWeb(t, webRoot(t))
	for _, p := range []string{"/api/v1/nope", "/api/nope", "/ws"} {
		code, body := get(r, p)
		if code != http.StatusNotFound {
			t.Errorf("%s = %d，想要 404", p, code)
		}
		if strings.Contains(body, "<!doctype html>") {
			t.Errorf("%s 回了 index.html —— 前端会拿这页 HTML 去 JSON.parse：%q", p, body)
		}
	}
}

// 没部署前端时说清楚缺的是什么，而不是回一个让人去猜的 404。
//
// **「没部署前端」和「前端崩了」要分得开。** 主控可以只跑 API
// （灰度时先起后端是常见做法）。
func TestMissingWebRootExplainsItself(t *testing.T) {
	r, _ := newServerWithWeb(t, "")

	code, body := get(r, "/")
	if code != http.StatusNotFound {
		t.Fatalf("/ = %d", code)
	}
	for _, want := range []string{"EC_WEB_ROOT", "静态文件", "API 不受影响"} {
		if !strings.Contains(body, want) {
			t.Errorf("应当说清缺什么、怎么补，缺少 %q：%s", want, body)
		}
	}

	// API 照常。
	if code, _ := get(r, "/api/v1/nope"); code != http.StatusNotFound {
		t.Errorf("没有前端时 API 仍该正常路由，实际 %d", code)
	}
}

// **不能用 `..` 爬出 WebRoot。**
//
// 防护是**双层**的，而这一点是用探针试出来的、不是设计时想清楚的：
// 存在性检查走 `http.Dir(root).Open(path.Clean(p))`，实际伺服走
// `http.FileServer(http.Dir(root))` —— **两层各自都足够**。
//
// 所以验这条测试有效需要**同时改坏两层**：单改一层时另一层会兜住，
// 探针跑出绿色，而那绿色会被读成「这条测试是摆设」。
// 我为此打偏了三次（改存在性检查、改伺服、改成手写 os.ReadFile），
// 每次都是另一层救了它。
//
// 两层一起换成手写拼接之后立刻红——**那才是这条测试真正守着的东西：
// 至少有一层在**。
func TestCannotEscapeWebRoot(t *testing.T) {
	dir := webRoot(t)
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("DO-NOT-SERVE"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := newServerWithWeb(t, dir)

	for _, p := range []string{
		"/../secret.txt",
		"/assets/../../secret.txt",
		"/%2e%2e/secret.txt",
	} {
		_, body := get(r, p)
		if strings.Contains(body, "DO-NOT-SERVE") {
			t.Errorf("%s 爬出了 WebRoot：%q", p, body)
		}
	}
}

func get(r http.Handler, path string) (int, string) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// **`/assets/` 下找不到就 404，不 fallback。**
//
// 前端 agent 用他自己的产物验出来的：`/assets/nonexistent.js` 回了
// 200 + `text/html`。判据跟 `/api/*` 完全一样——**那个路径下的东西
// 只有一种消费者，而那个消费者不认识 HTML**。
//
// 后果比 API 那边更难查：浏览器会拿一整页 HTML 当 JavaScript 执行，
// 报出来的是 `Unexpected token '<'`，**而真正的问题是那个文件不在**。
// 人会去看那个文件的语法，而它的语法完全正确。
//
// 灰度上这很容易发生：包传了一半、`index.html` 与 `assets/` 版本不匹配
// （前端的文件名带 hash，正是为了让这两者不会静默错配）。
func TestMissingAssetIs404NotIndexHTML(t *testing.T) {
	r, _ := newServerWithWeb(t, webRoot(t))

	code, body := get(r, "/assets/nonexistent.js")
	if code != http.StatusNotFound {
		t.Errorf("/assets/nonexistent.js = %d，想要 404", code)
	}
	if strings.Contains(body, "<!doctype html>") {
		t.Errorf("回了 index.html —— 浏览器会拿这页 HTML 当 JS 执行，"+
			"报 Unexpected token '<'，而真正的问题是文件不在：%q", body)
	}

	// **而带点的 SPA 路由仍然要 fallback。**
	//
	// 这一条是这个判据为什么按**目录**而不按**扩展名**：工作台的路径里
	// 有资源 key（`route:api.example.com`），它带点、也带冒号。
	// 一个「路径里有点就不 fallback」的规则会把它误伤成 404，
	// 而那是人刷新页面时最常撞上的路径之一。
	for _, p := range []string{
		"/workbench/route:api.example.com",
		"/workbench/global:tls",
	} {
		if code, body := get(r, p); code != 200 || !strings.Contains(body, "<!doctype html>") {
			t.Errorf("%s 应当 fallback，实际 %d %q", p, code, body)
		}
	}
}

// **「不在」和「读不到」必须说成两句话。**
//
// 灰度上真实发生过：文件明明在（root 跑 `ls` 看得见 index.html、assets/），
// 而主控咬定「静态文件不在」。真因是 tar 包顶层那个 `./` 带着 0700，
// 解包时盖到了目标目录上，而主控跑在 `User=edge` 下——连目录都进不去。
//
// 原先的实现把 os.Stat 的 err 整个丢掉了，两种错走同一句话。
//
// **一句错误的诊断比没有诊断更贵：它给了人一个方向，而那个方向是反的。**
// 「文件不在」把人送去 `ls`，`ls`（用 root 跑）显示文件都在，
// 于是他去查解包、查路径、查版本——唯独不会去查权限，因为主控已经说了。
//
// 这条测试钉的是那个**分岔**，不是文案：报错里必须出现「权限」这个方向，
// 且必须提醒 root 的 `ls` 说明不了问题。
func TestUnreadableWebRootSaysPermissionNotMissing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("以 root 跑时权限位不起作用，这条测不出东西 —— " +
			"跳过而不是让它假绿")
	}
	dir := webRoot(t)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // 不还原 t.TempDir 删不掉

	r, _ := newServerWithWeb(t, dir)
	code, body := get(r, "/")
	if code != http.StatusNotFound {
		t.Fatalf("/ = %d", code)
	}
	if strings.Contains(body, "静态文件不在") {
		t.Errorf("读不到不等于不在 —— 这句话会把人送去 ls，"+
			"而 ls 会告诉他文件都在：%s", body)
	}
	for _, want := range []string{"权限", "ls", "EC_WEB_ROOT"} {
		if !strings.Contains(body, want) {
			t.Errorf("报错里缺 %q —— 排查方向指不出来：%s", want, body)
		}
	}

	// 目录恢复可读之后，同一个请求要能正常拿到 index.html。
	// **没有这一条，上面那些断言在「serveWeb 对所有请求都报权限错」时也全绿。**
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, body := get(r, "/"); code != http.StatusOK || !strings.Contains(body, "<!doctype") {
		t.Fatalf("权限恢复之后应当正常伺服 index.html，实际 %d %q", code, body)
	}
}
