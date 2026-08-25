package api_test

import (
	"encoding/json"
	"github.com/xltxb/edge_caddy/internal/store"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/api"
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
	// §2 节点隧道 —— **不是给浏览器的**，Agent 用它穿 443。
	// 挂在鉴权组外面是有意的：认证在里层那次 mTLS 握手里（ADR-0009）。
	"GET /api/v1/tunnel",
	// §3 总览
	"GET /api/v1/overview",
	// §4 边缘节点
	"GET /api/v1/nodes",
	"POST /api/v1/nodes/token",
	"GET /api/v1/nodes/:id/logs",
	"POST /api/v1/nodes/:id/push",
	"POST /api/v1/nodes/:id/dns",
	"POST /api/v1/nodes/:id/probe",
	"POST /api/v1/nodes/:id/drain",
	"POST /api/v1/nodes/:id/rejoin",
	"PUT /api/v1/nodes/:id",
	"DELETE /api/v1/nodes/:id",
	// §6 配置资源
	"GET /api/v1/routes",
	"POST /api/v1/routes",
	"PUT /api/v1/routes/:domain",
	"DELETE /api/v1/routes/:domain",
	"GET /api/v1/rules",
	"PUT /api/v1/rules/:id",
	"DELETE /api/v1/rules/:id",
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
	"PUT /api/v1/certs/:domain",
	"DELETE /api/v1/certs/:domain",
	// §10 审计
	"GET /api/v1/audit",
	// §11 设置与告警
	"GET /api/v1/settings",
	"PUT /api/v1/settings",
	"GET /api/v1/alerts",
	"PUT /api/v1/alerts",
	"POST /api/v1/alerts/test",
}

// unimplementedEndpoints 是**契约里写了、刻意没有实现**的端点。
//
// 「未实现的端点不注册，也不给返回空数据的桩」是个刻意的决定（契约 §0）：
// 桩会被读成「还没有数据」，404 才说得出「这个端点还没做」。
//
// 但那条原则此前没有任何东西守着，于是 GET /nodes/:id/logs 在契约里躺了很久
// ——格式完整、看不出异样、从来没注册过，而 contractEndpoints 那张**手工维护**
// 的清单漏了它。**一份用来防止人忘记的清单，自己被忘了。**
//
// 现在它进清单，但进的是这一格：下面那条测试断言它**确实没有注册**。
// 目前是空的 —— 契约里每一个端点都实现了。
//
// 留着这个清单和它下面那条测试，是因为「未实现的端点不注册也不给桩」
// （契约 §0）是个会被反复用到的原则，而下一次有人往契约里写一个还没做的
// 端点时，这里就是它该待的地方。
var unimplementedEndpoints = []string{}

// contractMentionExemptions 是契约全文里形如 `METHOD /path` 但**不是端点声明**
// 的片段。每一条都要写清为什么豁免 —— 一个没有理由的豁免列表会变成垃圾桶，
// 而垃圾桶里迟早会躺着一个真的遗漏。
var contractMentionExemptions = map[string]string{
	"PUT /routes/nope.com":                "错误码表里的例子，不是端点声明",
	"POST /deploys/:cfg_version/rollback": "同一端点的另一种写法，实现里参数名是 :id",

	// 这是 **Cloudflare 的** URL，不是我们的。契约里引它是为了说明
	// account_id 为空时那个双斜杠长什么样 —— 扫描器分不出上游和自家，
	// 而分不出是对的：它宁可多问一句，也不该猜。
	"GET /accounts//load_balancers/pools": "上游（Cloudflare）的路径，出现在 account_id 必填那段的病症描述里",
	"PUT /rules/x":                        "§6.2 里举的例子（停用 + 空 ips 存得进去），:id 位置是个占位的 x",
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
// **这是一条否定断言，它天然会因为装置失效而变绿。**
//
// 「路由表里没有多余的端点」和「我根本没拿到路由表」产生同样的结果——
// r.Routes() 哪天返回空（装配变了、gin 的 API 变了），这条会一声不响地全绿。
//
// 旁边的 TestAllContractEndpointsAreRegistered 是肯定断言，装置坏了它会红，
// 所以这两条实际上互为对方的兜底。**但那是偶然的**：这条测试自己读起来完全独立，
// 谁把另一条删了或改窄了，都不会觉得跟这里有关系。
//
// 所以自己也带一句自检。前端 agent 给这个形状起了个名字，值得照抄：
// **否定断言天然会因为装置失效而变绿。**
// **契约里提到的端点，都要在上面那两张清单之一里。**
//
// 这条守的是那两张清单本身。它们是手工维护的，而 GET /nodes/:id/logs 证明了
// 手工维护的清单会被忘 —— 忘掉之后，「契约与实现一致」这件事就没人在看了，
// 而两条端点测试照旧全绿。
//
// 从契约**自动生成**清单更彻底，但这份文档里端点的写法不统一
// （有的是 ### 标题，有的在正文里、在错误码表的例子里），
// 全自动会把例子当成端点、把变体当成遗漏。所以是宽松扫描 + 具名豁免。
func TestEveryEndpointMentionedInContractIsAccountedFor(t *testing.T) {
	raw, err := os.ReadFile("../../docs/api-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	mentions := regexp.MustCompile("`(GET|POST|PUT|DELETE|PATCH) (/[^`\\s]*)`").
		FindAllStringSubmatch(string(raw), -1)
	// 装置自检：这份文档理应提到几十个端点。解析不出来的话，
	// 下面那个「都在清单里」会因为什么都没扫到而成立。
	if len(mentions) < 30 {
		t.Fatalf("只从契约里扫出 %d 处端点提及 —— 这份文档不是我们以为的东西，"+
			"下面的断言什么也没在检查", len(mentions))
	}

	known := map[string]bool{}
	for _, e := range append(append([]string{}, contractEndpoints...), unimplementedEndpoints...) {
		known[e] = true
	}
	var missing []string
	for _, m := range mentions {
		key := m[1] + " /api/v1" + m[2]
		if known[key] || contractMentionExemptions[m[1]+" "+m[2]] != "" {
			continue
		}
		missing = append(missing, key)
	}
	if len(missing) > 0 {
		t.Errorf("契约里提到这些端点，而两张清单都没有它们：\n  %s\n"+
			"要么加进 contractEndpoints（已实现），要么加进 unimplementedEndpoints"+
			"（刻意没做），要么加进 contractMentionExemptions 并写清为什么不是端点。",
			strings.Join(missing, "\n  "))
	}
}

// **刻意未实现的端点，必须确实没有注册。**
//
// 「不给返回空数据的桩」这条原则此前只写在契约里。写在文档里的原则
// 不会红，而一个悄悄加上的桩会让前端读成「还没有数据」。
func TestUnimplementedEndpointsAreReallyAbsent(t *testing.T) {
	r, _ := newServer(t)
	have := registered(r)
	for _, e := range unimplementedEndpoints {
		if have[e] {
			t.Errorf("%s 标着未实现，却注册了 —— 要么把它从 unimplementedEndpoints "+
				"挪进 contractEndpoints，要么它是个不该存在的桩", e)
		}
	}
}

// **列表项的字段集要与契约 §7.3 那张表一致。**
//
// `GET /deploys` 的列表项是**直接序列化仓储结构体**的，而详情是手工拼的
// ——于是给 store.Deploy 加一个字段，列表会**静默多出一个契约没写的键**，
// 而没有任何东西会红。`targets` 就是这么漏出去的（前端拿 mock 与真主控
// 比形状才发现）。
//
// 这条钉的是那个漏法本身：结构体变了、契约没跟，这里就红。
func TestDeployListItemFieldsMatchContract(t *testing.T) {
	// 契约 §7.3 那张表里的键。
	want := map[string]bool{
		"id": true, "cfg_version": true, "operator": true, "res_keys": true,
		"ok_count": true, "fail_count": true, "targets": true,
		"is_baseline": true, "created_at": true,
	}

	b, err := json.Marshal(store.Deploy{})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	// 装置自检：序列化得真的产出了东西，否则下面两个循环都会空转。
	if len(got) < 5 {
		t.Fatalf("只序列化出 %d 个键 —— 这不是我们以为的东西", len(got))
	}

	for k := range got {
		if !want[k] {
			t.Errorf("列表项多出一个契约 §7.3 没写的键 %q —— "+
				"给 store.Deploy 加字段会静默漏进这个响应", k)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("契约 §7.3 写了 %q 而列表项没有它", k)
		}
	}
}

func TestNoUndocumentedEndpoints(t *testing.T) {
	r, _ := newServer(t)
	if n := len(r.Routes()); n < len(contractEndpoints) {
		t.Fatalf("路由表只有 %d 条，比契约里的 %d 条还少 —— "+
			"这份路由表不是我们以为的东西，下面那个『没有多余端点』的结论"+
			"是因为什么都没看到才成立的", n, len(contractEndpoints))
	}
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

// **`GET /overview` 必须说得出此刻在跑的是哪一版。**
//
// 它此前只出现在启动日志里，而看得到日志的人和验行为的人常常不是同一个。
// 前端 agent 在灰度上复验一个修复时卡住的正是这一点：他看到旧行为，
// 而**「修得不对」和「根本没部署」产生的观测一模一样**。
//
// 这条测试也钉住「没注入时是 dev，不是空串」：空串会在界面上显示成一片空白，
// 而空白读起来是「这个字段还没做」，不是「这是个未打标的构建」。
func TestOverviewReportsMasterVersion(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)

	_, e := do(t, r, "GET", "/api/v1/overview", nil,
		func(req *http.Request) { req.AddCookie(ck) })
	if e.Code != api.CodeOK {
		t.Fatalf("code=%d msg=%s", e.Code, e.Msg)
	}
	var d struct {
		MasterVersion *string `json:"master_version"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.MasterVersion == nil {
		t.Fatal("总览里没有 master_version —— " +
			"没有它，「修得不对」和「根本没部署」在界面上是同一个样子")
	}
}

// **`GET /nodes` 的字段集要与契约 §2 那张表一致。**
//
// 这个端点的字段最多，而它还在长（今天一天里加了 reconnects_1h、
// dns_reason / dns_actor / dns_changed_at、geo_db_ok）。
//
// 而它此前**没有任何东西比对过形状**：前端那套 mock-对-真主控的检查里
// 没有它——那不是「检查漏报」，是压根没检查。这一条补的是后端这一半：
// **结构体加了字段而契约没跟，这里就红。**
//
// 它管不到的：字段的**类型**变了（string → int），以及前端那份类型定义
// 跟不跟得上。前者留给契约里的示例，后者是前端那条检查的事。
// 写出来是为了让下一个人知道这一条守到哪儿为止。
func TestNodeListFieldsMatchContract(t *testing.T) {
	// 契约 §2 那张表里的键。
	want := map[string]bool{
		"id": true, "city": true, "vendor": true, "line": true,
		"public_ip": true, "status": true, "online": true,
		"reconnects_1h": true, "cpu": true, "mem": true, "conns": true,
		"cpu_series": true, "last_hb_at": true, "hb_age_ms": true,
		"cfg_version": true, "drift": true, "dns_enabled": true,
		"drained_at": true, "dns_reason": true, "dns_actor": true,
		"dns_changed_at": true, "agent_version": true, "geo_db_ok": true,
		"blocked_1h": true,
		"in_rotation": true, "weight_set": true,
		"routes":     true, "rules": true, "created_at": true,
	}

	b, err := json.Marshal(api.NodeRespForTest())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	// 装置自检：序列化得真的产出了东西，否则下面两个循环都会空转。
	if len(got) < 20 {
		t.Fatalf("只序列化出 %d 个键 —— 这不是我们以为的东西", len(got))
	}

	for k := range got {
		if !want[k] {
			t.Errorf("节点项多出一个契约 §2 没写的键 %q —— "+
				"给 nodeResp 加字段会静默漏进这个响应，而前端那套形状检查"+
				"里没有这个端点", k)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("契约 §2 写了 %q 而节点项没有它", k)
		}
	}
}

// **`{res_key, field, reason}` 这个形状要与契约 §0.3 一致。**
//
// 它有两个消费方：`1002` 的 `errors`，和 `GET /rules` 的 `incomplete` ——
// 而两边的界面都照契约写。改一个键名，两处一起哑掉，
// 而**接口照常返回 200**，只是界面上那些原因不再显示。
//
// 前端指出这个形状**目前没有任何一处在比**：他那套形状检查跳过了
// `incomplete`（它是 ruleId → 清单的 map，比键等于比「哪些规则 id 恰好
// 两边都有」，那是巧合不是形状）。这一条补的是后端这一半。
func TestFieldErrorKeysMatchContract(t *testing.T) {
	want := map[string]bool{"res_key": true, "field": true, "reason": true}

	b, err := json.Marshal(api.FieldErrorForTest())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("序列化出 %d 个键（期望 3）—— 这不是我们以为的东西：%v", len(got), got)
	}
	for k := range got {
		if !want[k] {
			t.Errorf("多出一个契约 §0.3 没写的键 %q", k)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("契约 §0.3 写了 %q 而它没有 —— "+
				"1002 的 errors 与 GET /rules 的 incomplete 会一起哑掉，"+
				"而接口照常返回 200", k)
		}
	}
}
