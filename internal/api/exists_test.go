package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
)

// TestExistenceChecksSayWhichStepFailed：库挂了不等于「这条资源在」。
//
// 三处「这条资源在不在」的检查只判 ErrNotFound，其余 err 直接往下走
// （issue #66）：
//
//	if _, err := s.store.GetRoute(ctx, domain); errors.Is(err, store.ErrNotFound) {
//	    Fail(c, CodeNotFound, "没有这条路由"); return
//	}
//	// 连接失败、超时 —— 继续往下写
//
// 而同一个文件的 handleCreateRoute 是对的（`else if !errors.Is(...)` 分了两支）。
// **四处同形状，三处漏了同一支。**
//
// # 判据是「报错说出了哪一步失败」
//
// 走下去之后那次写入同样会失败，所以人看得到一个错——但那个错说的是
// 「修改路由失败」，而真正倒下的是它前面那次读。domain.md 记过这个形状：
// 「一个只报叶子的错误信息，会让所有人只看叶子——包括写它的人」。
//
// 更糟的情形这条测试造不出来：读是**瞬时**失败而随后的写成功时，
// `PUT /routes/:domain` 会把一条不存在的路由 upsert 出来——「改」变成了「建」。
// 那需要往 store 里注入一次性故障，而这一层拿到的是一个真 *store.Store。
//
// 用 ops-bot 的 Bearer 而不是 Cookie：会话查询要读库，而库已经没了，
// 那条路上请求根本到不了 handler（#46 修完之后它会在中间件就停下）。
func TestExistenceChecksSayWhichStepFailed(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   any
		// wrong 是「走下去之后那一步」的措辞——出现它就说明前面那次读被无视了。
		wrong string
	}{
		{"改路由", http.MethodPut, "/api/v1/routes/a.example.com",
			map[string]any{"upstream": "127.0.0.1:8080", "block_mode": "abort"}, "修改路由失败"},
		{"删路由", http.MethodDelete, "/api/v1/routes/a.example.com", nil, "删除路由失败"},
		{"删规则", http.MethodDelete, "/api/v1/rules/r1", nil, "删除访问规则失败"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, st := newServer(t)
			st.Pool.Close() // 库没了

			w, env := do(t, r, c.method, c.path, c.body, func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+opsBotToken)
			})
			if env.Code == api.CodeOK {
				t.Fatalf("库都没了却回了成功（http=%d）", w.Code)
			}
			if env.Code == api.CodeNotFound {
				t.Fatal("库挂了被报成「没有这条资源」—— " +
					"人会去查这条资源是不是被别人删了，而问题不在那儿")
			}
			if strings.Contains(env.Msg, c.wrong) {
				t.Errorf("报的是 %q —— 那是下一步的措辞，而真正倒下的是它前面那次读；"+
					"人会照着这句话去查写入那一侧", env.Msg)
			}
		})
	}
}
