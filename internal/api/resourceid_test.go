package api_test

import (
	"net/http"
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
)

// **资源 ID 的格式是接口的一部分，不是界面偏好**（契约 §0.7）。
//
// `node_id` 与规则 `id` 都会被拼进 URL 路径、进 DNS 记录的比对键、
// 进证书的 CN（ADR-0009：node_id 就写在隧道证书的 CN 里）。带 `/` 或 `#`
// 的 id 在其中任何一处上都会安静地走偏，而症状出现在离输入点很远的地方。
//
// 控制台有这道闸（`web/src/ids.ts`），而**那不够**：契约 §0.6 明说直改路
// （ops-bot、批量脚本）是给脚本的入口，它绕过控制台。这个仓库已经数过
// 好几次同一个形状——前端拦住不等于后端安全，#70 的 If-None-Match 是
// 上一次。
func TestTokenRefusesMalformedNodeID(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)

	for _, id := range []string{
		"node/../etc", // 拼进 URL 路径会走到别处
		"node#1",      // 片段分隔符
		"node id",     // 空格
		"NODE-HK-01",  // 大写：证书 CN 与 DNS 比对键都区分大小写
		"a",           // 太短
		"-node-hk-01", // 连字符开头
		"节点一",         // 非 ASCII
		"",            // 空
	} {
		t.Run(id, func(t *testing.T) {
			_, env := do(t, r, "POST", "/api/v1/nodes/token", map[string]string{
				"node_id": id, "city": "香港", "vendor": "DMIT",
				"line": "CN2 GIA", "public_ip": "203.0.113.7",
			}, func(req *http.Request) { req.AddCookie(ck) })

			// **断言落在具体的 code 上，不是「非 0」。** 契约 §0.7 写明拒的是
			// 1001，而「非 0」放得过 1002——那一档在 §0.3 里必须带结构化
			// errors、data 不为 null，与这里回的形状不是一回事。一条比意图宽的
			// 断言挡不住契约与实现分叉，而契约是前端照着写的那一份。
			if env.Code != api.CodeBadParam {
				t.Fatalf("node_id %q 得到 code=%d，契约 §0.7 说拒 1001 —— "+
					"它会进证书的 CN 与九处 URL 路径，而签发 Token 之后"+
					"人就去跑安装脚本了", id, env.Code)
			}
		})
	}
}

// 正向的一半：一个合法的 id 照样过得去。
// 没有这一条，一个「无条件拒绝所有 node_id」的实现也能让上面全过。
func TestTokenStillAcceptsAWellFormedNodeID(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)

	_, env := do(t, r, "POST", "/api/v1/nodes/token", map[string]string{
		"node_id": "node-hk-01", "city": "香港", "vendor": "DMIT",
		"line": "CN2 GIA", "public_ip": "203.0.113.7",
	}, func(req *http.Request) { req.AddCookie(ck) })
	if env.Code != 0 {
		t.Fatalf("node-hk-01 是契约 §0.7 里的合法形状，却被拒了：%s", env.Msg)
	}
}

func TestPutRuleRefusesMalformedID(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)

	// **只列 URL 路径上到得了的形状。** 带斜杠或空格的 id 在 httptest 那一层
	// 就构造不出请求，gin 的 `:id` 也只匹配一段——那类东西到不了 handler，
	// 为它们写断言等于测试一件别处已经保证的事。
	for _, id := range []string{"SVC-1", "s", "-svc", "svc_1", "规则一"} {
		t.Run(id, func(t *testing.T) {
			_, env := do(t, r, "PUT", "/api/v1/rules/"+id, map[string]any{
				"id": id, "name": "测试", "type": "ip_whitelist",
				"enabled": true, "apply_to": []string{},
				"spec": map[string]any{"ips": []string{"203.0.113.0/24"}},
			}, func(req *http.Request) { req.AddCookie(ck) })

			if env.Code != api.CodeBadParam {
				t.Fatalf("规则 id %q 得到 code=%d，契约 §0.7 说拒 1001 —— "+
					"它会被拼进 URL 路径，而删除那条路走的是同一个路径", id, env.Code)
			}
		})
	}
}
