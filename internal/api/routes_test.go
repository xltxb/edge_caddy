package api_test

import (
	"os"
	"regexp"
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
var unimplementedEndpoints = []string{
	// 要实现它得有整条日志链路（Agent 上报 LogBatch → 主控存 → 这个端点查），
	// 而 proto 里的 LogBatch 至今也没人接。
	"GET /api/v1/nodes/:id/logs",
}

// contractMentionExemptions 是契约全文里形如 `METHOD /path` 但**不是端点声明**
// 的片段。每一条都要写清为什么豁免 —— 一个没有理由的豁免列表会变成垃圾桶，
// 而垃圾桶里迟早会躺着一个真的遗漏。
var contractMentionExemptions = map[string]string{
	"PUT /routes/nope.com":                "错误码表里的例子，不是端点声明",
	"POST /deploys/:cfg_version/rollback": "同一端点的另一种写法，实现里参数名是 :id",
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
