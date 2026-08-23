package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
)

// certBotRoutes 是 cert-bot **应当**能到达的端点。与实现里那张白名单
// 分开写是有意的：两处都写一遍，改一处忘另一处时这条会红。
//
// **安全边界上，「两份知识」的代价小于「一份知识改错了没人发现」。**
// 别的地方我一直在消灭抄件，这里刻意留一份 —— 判据不同：
// 那些抄件分叉的后果是行为不一致，这一份分叉的后果是**权限悄悄变大**。
var certBotShouldReach = []string{
	"GET /api/v1/routes",
	"PUT /api/v1/certs/:domain",
}

// TestCertBotCannotReachAnythingElse 是这个 token 存在的**全部理由**。
//
// ops-bot 按设计是宽的（契约 §7：直改路是给批量脚本的），它能删节点、
// 下线节点、改 DNS 凭证、下发配置。把它交给外部证书平台，
// **等于对方那边一次日志泄露就是我们整个控制面**。
//
// 没有这一条，cert-bot 就只是「另一个名字的 ops-bot」——
// 而那种东西比没有它更糟：它让人以为自己收窄了权限。
func TestCertBotCannotReachAnythingElse(t *testing.T) {
	r, _ := newServer(t)

	allowed := map[string]bool{}
	for _, k := range certBotShouldReach {
		allowed[k] = true
	}

	checked := 0
	for _, ri := range r.Routes() {
		key := ri.Method + " " + ri.Path
		if allowed[key] || !strings.HasPrefix(ri.Path, "/api/v1/") {
			continue
		}
		// **只检查真的受会话保护的路由。**
		//
		// 判据是「不带任何凭据时回 401」——那说明它在 Auth 后面。
		// 登录端点、以及节点隧道那个 WebSocket 升级点都不在（后者用 mTLS
		// 在连接里面认，本来就不该要会话），它们回 200 不是越权。
		//
		// 用这个判据而不是写一张豁免名单：名单要人维护，
		// 而**加一个新的免鉴权端点时没人会想起来更新它**。
		bare, _ := http.NewRequest(ri.Method, ri.Path, nil)
		bw := httptest.NewRecorder()
		r.ServeHTTP(bw, bare)
		if bw.Code != http.StatusUnauthorized {
			continue
		}
		checked++
		req, _ := http.NewRequest(ri.Method, ri.Path, nil)
		req.Header.Set("Authorization", "Bearer "+certBotToken)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s：cert-bot 到达了它不该到的端点（HTTP %d，期望 403）",
				key, w.Code)
		}
	}

	// 装置自检：一条都没检查的话上面是空转。
	if checked < 15 {
		t.Fatalf("只检查了 %d 个端点 —— 路由表不是我们以为的东西，"+
			"上面那个「都被拒了」的结论是因为什么都没看到才成立的", checked)
	}
	t.Logf("检查了 %d 个端点，cert-bot 全部拿到 403", checked)
}

// TestCertBotCanReachWhatItNeeds 是反面。
//
// 没有这一条，一个「无条件 403」的实现也能让上面全绿，
// 而那个 token 就什么也做不了 —— 一条只会拒绝的边界和没有边界一样没用。
func TestCertBotCanReachWhatItNeeds(t *testing.T) {
	r, _ := newServer(t)
	for _, key := range certBotShouldReach {
		method, path, _ := strings.Cut(key, " ")
		req, _ := http.NewRequest(method, strings.Replace(path, ":domain", "x.example.com", 1), nil)
		req.Header.Set("Authorization", "Bearer "+certBotToken)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
			t.Errorf("%s：cert-bot 到不了它必须到的端点（HTTP %d）", key, w.Code)
		}
	}
}

// TestCertBotShouldReachMatchesRouter：白名单里的端点必须真的存在。
//
// **一条指向不存在的路由的白名单规则，是一条永远不会生效的规则** ——
// 而它看起来和生效的一模一样。端点改名时这条会红。
func TestCertBotShouldReachMatchesRouter(t *testing.T) {
	r, _ := newServer(t)
	have := map[string]bool{}
	for _, ri := range r.Routes() {
		have[ri.Method+" "+ri.Path] = true
	}
	for _, k := range certBotShouldReach {
		if !have[k] {
			t.Errorf("白名单里有 %q，而路由表里没有 —— 这条规则永远不会生效", k)
		}
	}
	_ = api.CodeOK
}
