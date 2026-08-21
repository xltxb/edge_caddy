package api_test

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// contractEndpoints 是 docs/api-contract.md 列出的全部端点。
//
// 这张表存在的理由：我曾经口头宣布「端点都在了」，而 /rules 与 /policies/:id
// 从来没注册过——是前端切过去撞了 404 才发现的。
//
// 一个人的记忆不该是这件事的保障。改契约时同时改这张表，忘了改就会红。
var contractEndpoints = []string{
	// §1 会话
	"POST /api/v1/auth/login",
	"POST /api/v1/auth/logout",
	"GET /api/v1/auth/session",
	// §2 实时
	"GET /api/v1/ws",
	// §3 总览
	"GET /api/v1/overview",
	// §4 边缘节点
	"GET /api/v1/nodes",
	"POST /api/v1/nodes/token",
	"POST /api/v1/nodes/:id/push",
	"POST /api/v1/nodes/:id/dns",
	"POST /api/v1/nodes/:id/probe",
	"POST /api/v1/nodes/:id/drain",
	"POST /api/v1/nodes/:id/rejoin",
	// §6 配置资源
	"GET /api/v1/routes",
	"POST /api/v1/routes",
	"PUT /api/v1/routes/:domain",
	"DELETE /api/v1/routes/:domain",
	"GET /api/v1/rules",
	"PUT /api/v1/rules/:id",
	"GET /api/v1/policies/:id",
	"PUT /api/v1/policies/:id",
	"GET /api/v1/drafts",
	"PUT /api/v1/drafts/:key",
	"DELETE /api/v1/drafts",
	// §7 下发
	"POST /api/v1/deploys/preview",
	"POST /api/v1/deploys",
	"GET /api/v1/deploys",
	"GET /api/v1/deploys/:id",
	"POST /api/v1/deploys/:id/rollback",
	// §8 DNS
	"GET /api/v1/dns/weights",
	"PUT /api/v1/dns/weights",
	// §9 证书
	"GET /api/v1/certs",
	"POST /api/v1/certs/:domain/renew",
	"POST /api/v1/certs/renew-check",
	// §10 审计
	"GET /api/v1/audit",
	// §11 设置与告警
	"GET /api/v1/settings",
	"PUT /api/v1/settings",
	"GET /api/v1/alerts",
	"PUT /api/v1/alerts",
	"POST /api/v1/alerts/test",
}

func registered(r *gin.Engine) map[string]bool {
	out := map[string]bool{}
	for _, ri := range r.Routes() {
		out[ri.Method+" "+ri.Path] = true
	}
	return out
}

// 契约里的每个端点都必须真的注册了。
//
// 「未实现的端点不注册」是个刻意的决定（返回空数据的桩会被读成「还没有数据」，
// 404 才说得出「这个端点还没做」）。但那说的是**尚未开工**的端点；
// 一个已经宣布完成的契约条目缺席，是另一回事。
func TestAllContractEndpointsAreRegistered(t *testing.T) {
	r, _ := newServer(t)
	have := registered(r)

	var missing []string
	for _, want := range contractEndpoints {
		if !have[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("契约里有但路由表里没有：\n  %s", strings.Join(missing, "\n  "))
	}
}

// 反过来也要查：路由表里有而契约里没有的端点，要么是忘了写进契约，
// 要么是不该存在。两种都值得当场知道。
func TestNoUndocumentedEndpoints(t *testing.T) {
	r, _ := newServer(t)
	want := map[string]bool{}
	for _, e := range contractEndpoints {
		want[e] = true
	}

	var extra []string
	for _, ri := range r.Routes() {
		key := ri.Method + " " + ri.Path
		if !want[key] {
			extra = append(extra, key)
		}
	}
	if len(extra) > 0 {
		t.Fatalf("路由表里有但契约里没有（忘了写进契约，还是不该存在？）：\n  %s",
			strings.Join(extra, "\n  "))
	}
}
