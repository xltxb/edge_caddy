package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
)

// **写错的 key 必须报错，不能静默忽略。**
//
// gin 的 ShouldBindJSON 会丢掉未知字段，于是一个写错的 key 得到 `code: 0`
// ——**请求成功了，而什么也没存进去**。
//
// 前端 agent 就是这么撞上的：他发的是顶层 `dns_credential`，
// 而后端要的是 `dns_provider.credential`。返回 code 0、界面提示
// 「设置已保存」，而 `configured` 一直是 false。
//
// **这是「没生效」那一族里最坏的一种：成功的假象。**
// 报错会让人再试，假象让人走开——他会去查别的地方，
// 因为「保存那一步明明成功了」。
func TestUnknownFieldIsRejectedNotIgnored(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)
	auth := func(req *http.Request) { req.AddCookie(ck) }

	// 前端真实发过的那个形状。
	_, e := do(t, r, "PUT", "/api/v1/settings",
		map[string]any{"dns_credential": "ID,Token"}, auth)
	if e.Code == api.CodeOK {
		t.Fatal("写错的 key 得到了 code 0 —— 那是成功的假象，" +
			"人会以为存进去了然后去查别的地方")
	}
	// 报错里要**点名那个字段**：人多半是把嵌套的 key 写成了顶层，
	// 而他要的信息是「哪个字段不对」，不是「格式错误」。
	if !strings.Contains(e.Msg, "dns_credential") {
		t.Errorf("要点名那个写错的字段：%q", e.Msg)
	}

	// 而正确的形状照常工作，并且真的存进去了。
	_, ok := do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{
			"kind": "dnspod", "domain": "example.com", "sub": "cdn",
			"credential": "ID,Token",
		},
	}, auth)
	if ok.Code != api.CodeOK {
		t.Fatalf("正确的形状不该被拒：code=%d msg=%q", ok.Code, ok.Msg)
	}
	_, got := do(t, r, "GET", "/api/v1/settings", nil, auth)
	if !strings.Contains(string(got.Data), `"configured":true`) {
		t.Errorf("凭证应当真的存进去了：%s", got.Data)
	}
}

// **凭证要有一条删除路径。**
//
// 空串对凭证是「不改动」（凭证不回显，前端带不出原值），所以逐字段清空之后
// 会留下一个**能到达的矛盾状态**：库里有凭证、而没有服务商。
// 前端 agent 在真主控上撞到了它——横幅说「还没配」，凭证徽标同时说「已配置」，
// 两句话都在页面上，而两句都对。
//
// 一份再也用不到、也删不掉的凭证仍然是一把**有效的 API Token**。
func TestClearRemovesProviderAndCredential(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)
	auth := func(req *http.Request) { req.AddCookie(ck) }

	_, e := do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{
			"kind": "dnspod", "domain": "example.com", "sub": "cdn",
			"credential": "ID,Token",
		},
	}, auth)
	if e.Code != api.CodeOK {
		t.Fatalf("先配上：code=%d msg=%q", e.Code, e.Msg)
	}

	// **逐字段清空清不掉凭证** —— 这是那个矛盾状态怎么来的。
	do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{"kind": "", "domain": "", "sub": ""},
	}, auth)
	_, mid := do(t, r, "GET", "/api/v1/settings", nil, auth)
	if !strings.Contains(string(mid.Data), `"configured":true`) {
		t.Fatal("前置条件不成立：逐字段清空之后凭证本该还在（空串 = 不改动）")
	}

	// clear 才真的清掉。
	_, cl := do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{"clear": true},
	}, auth)
	if cl.Code != api.CodeOK {
		t.Fatalf("clear 应当成功：code=%d msg=%q", cl.Code, cl.Msg)
	}
	_, after := do(t, r, "GET", "/api/v1/settings", nil, auth)
	if !strings.Contains(string(after.Data), `"configured":false`) {
		t.Errorf("clear 之后凭证该没了：%s", after.Data)
	}
}

// **clear 与其他字段同时出现时拒绝，不静默取舍。**
//
// `{"clear":true,"kind":"dnspod"}` 有两种合理读法（先清再设 / 清掉一切），
// 而挑一种执行等于替人做了他没做的决定。
func TestClearRejectsBeingMixedWithOtherFields(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)
	auth := func(req *http.Request) { req.AddCookie(ck) }

	_, e := do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{"clear": true, "kind": "dnspod"},
	}, auth)
	if e.Code != api.CodeValidation {
		t.Fatalf("混着给应当被拒，实际 code=%d msg=%q", e.Code, e.Msg)
	}
}

// **`PUT /alerts` 的请求体是平的，跟 `GET` 的形状不一样。**
//
// 契约原先把两个端点并成一个代码块、一份 JSON，读起来就是「PUT 发 GET 那个形状」。
// 前端照着做了：`at_all_on_crit` 包在 `lark` 里发过来，而后端的结构体里它在顶层。
// ShouldBindJSON 静默丢掉，返回 code 0，界面显示「已保存」——
// **「严重时 @所有人」这个开关从来没存进去过**。
//
// 而 `notify_level` 恰好两边都在顶层，所以「改级别 → 保存 → 真的变了」验得通。
// **过的那一半掩护了没过的那一半**，这是这类 bug 最难被发现的形态。
//
// PUT 的字段集**必然**与 GET 不同：GET 里只有 `url_configured: true/false`，
// 凭证不回显，没有地方放 webhook 地址。既然必然不同，就让它明显不同。
func TestAlertsPutBodyIsFlatAndStrict(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)
	auth := func(req *http.Request) { req.AddCookie(ck) }

	// 一、平的形状真的存进去了——**特别是 at_all_on_crit**。
	_, ok := do(t, r, "PUT", "/api/v1/alerts", map[string]any{
		"notify_level":   "crit",
		"lark_webhook":   "https://open.larksuite.com/hook/x",
		"at_all_on_crit": true,
	}, auth)
	if ok.Code != api.CodeOK {
		t.Fatalf("平的形状不该被拒：code=%d msg=%q", ok.Code, ok.Msg)
	}
	_, got := do(t, r, "GET", "/api/v1/alerts", nil, auth)
	if !strings.Contains(string(got.Data), `"at_all_on_crit":true`) {
		t.Errorf("at_all_on_crit 应当真的存进去了：%s", got.Data)
	}
	if !strings.Contains(string(got.Data), `"webhook_configured":true`) {
		t.Errorf("lark_webhook 应当真的存进去了：%s", got.Data)
	}

	// 二、GET 的形状发过来要**当场被拒**，不能默默吞掉。
	//
	// 这条断言盯的不是「拒绝」本身，是**拒绝里点了名**：发错形状的人需要知道
	// 是哪个字段不认识，否则他会以为是整个请求格式坏了，去查别的地方。
	_, e := do(t, r, "PUT", "/api/v1/alerts", map[string]any{
		"notify_level": "warn",
		"lark":         map[string]any{"at_all_on_crit": false},
	}, auth)
	if e.Code == api.CodeOK {
		t.Fatal("GET 的形状得到了 code 0 —— at_all_on_crit 被丢掉了，" +
			"而界面会显示「已保存」")
	}
	if !strings.Contains(e.Msg, "lark") {
		t.Errorf("要点名那个不认识的字段：%q", e.Msg)
	}

	// 三、被拒的那次**什么也没改**。
	//
	// 这条是前两条都盖不住的：一个「先存一半、再报错」的实现能同时通过
	// 上面两条，而它留下的是最糟的状态——报了错，值却变了。
	_, after := do(t, r, "GET", "/api/v1/alerts", nil, auth)
	if !strings.Contains(string(after.Data), `"notify_level":"crit"`) {
		t.Errorf("被拒的请求不该改动任何东西，级别应当还是 crit：%s", after.Data)
	}
	if !strings.Contains(string(after.Data), `"at_all_on_crit":true`) {
		t.Errorf("被拒的请求不该改动任何东西：%s", after.Data)
	}
}

// **`ops_bot_token` 不是设置端点能改的东西。**
//
// 契约原先写的是「`PUT` 时不带凭证字段 = 保持不变；带了就是替换。
// `ops_bot_token_configured` 同理」——**「同理」是假的**。它只从环境变量
// `EC_OPS_BOT_TOKEN` 读，主控启动时装进鉴权中间件。前端照着那句话做了个输入框，
// 而这个端点是严格绑定的，于是用户一在那个框里打字，**整个设置保存就崩**。
//
// 不做成可改的，理由不是「还没做」：它是**免登录调用主控的凭证**。
// 让一个已登录会话去铸一把长期钥匙，跟改 CA、改监听地址是同一类事——
// 属于部署面，不属于控制台。所以这条测试钉的是**它被拒**，不是它还没实现。
func TestOpsBotTokenIsNotSettableViaAPI(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)
	auth := func(req *http.Request) { req.AddCookie(ck) }

	_, e := do(t, r, "PUT", "/api/v1/settings",
		map[string]any{"ops_bot_token": "whatever"}, auth)
	if e.Code == api.CodeOK {
		t.Fatal("ops_bot_token 不该能通过 API 设置 —— 它是免登录调用主控的凭证")
	}
	if !strings.Contains(e.Msg, "ops_bot_token") {
		t.Errorf("要点名那个字段：%q", e.Msg)
	}

	// 只读回显还在：界面需要知道「配没配」，只是改不了。
	_, got := do(t, r, "GET", "/api/v1/settings", nil, auth)
	if !strings.Contains(string(got.Data), "ops_bot_token_configured") {
		t.Errorf("只读回显应当还在：%s", got.Data)
	}
}

// **每一个绑定结构体的写端点都要拒未知字段，不是只有 PUT /settings。**
//
// `bindStrict` 写好之后，很长一段时间**只挂在一个端点上**——而它解决的那个问题
// （写错的 key 得到 code 0）在每个写端点上都成立。这是「机制建好了，
// 但没接到最该接的那些输出上」在我自己代码里的又一例，
// 而 `PUT /alerts` 就是从那个缺口漏过去的。
//
// 所以断言的对象是**每一个**，清单从 `requestBodies` 里来，
// 而那张表由 `TestEveryWriteRouteHasARequestBodySpec` 盯着与路由表一致。
// 新加一个写端点忘了用严格绑定，这条会红。
//
// 断言的不是「被拒了」而是**「报错里点了那个字段的名」**：
// 前者一个 404 也满足（路径参数是假的），后者只有 bindStrict 做得到。
//
// 顺带钉住一条顺序：**绑定要发生在任何依赖状态的检查之前**。
// 「你发了一个不存在的字段」不需要查库、不需要装配、不需要那台机器在线就能知道，
// 而先报状态问题会把人支到完全无关的方向去。
// closedPathParam 列出路径参数取值来自**闭集**的端点。见下面用它的地方。
var closedPathParam = map[string]string{
	"PUT /policies/:p": "tls", // 只有 tls 与 log 两条全局策略
}

func TestEveryWriteEndpointRejectsUnknownFields(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)
	auth := func(req *http.Request) { req.AddCookie(ck) }

	eps := api.StructBodyEndpoints()
	if len(eps) < 10 {
		t.Fatalf("装置坏了：只拿到 %d 个绑定结构体的写端点", len(eps))
	}

	const bogus = "字段名写错了"
	for _, ep := range eps {
		method, path, _ := strings.Cut(ep, " ")
		// 路径参数默认填**不存在**的值：绑定发生在查库之前，所以它们不需要真实存在，
		// 而填真实存在的值会让「先查库再绑定」的实现看起来也是对的。
		//
		// 例外是**闭集**的路径参数（`/policies/:id` 只有 tls 与 log 两个合法值）。
		// 那种校验不依赖任何状态，跟绑定同属「不查库就能判」的一层，谁先谁后都对；
		// 拿一个非法值去探它，量的就不再是这条测试声称要量的东西了。
		if v, ok := closedPathParam[ep]; ok {
			path = strings.ReplaceAll(path, ":p", v)
		} else {
			path = strings.ReplaceAll(path, ":p", "不存在的东西")
		}
		// 登录是唯一一个不带会话的写端点——**这是「不发 Cookie」，不是「跳过」**。
		//
		// 这里原先写的是 `continue`，理由注成「单独由 TestUnknownFieldIsRejectedNotIgnored
		// 一族覆盖」。那句话是假的：那条测试验的是 PUT /settings。
		// 于是登录的严格绑定**一条测试都没有**，而清单里它看起来是被数过的。
		// **一个带着理由的豁免，比没有豁免更难被怀疑。**
		send := auth
		if ep == "POST /auth/login" {
			send = nil
		}
		_, e := do(t, r, method, "/api/v1"+path, map[string]any{bogus: 1}, send)
		if e.Code == api.CodeOK {
			t.Errorf("%s：发了一个契约里没有的字段，却得到 code 0 —— "+
				"那个值被静默丢掉了，而调用方会以为成功了", ep)
			continue
		}
		if !strings.Contains(e.Msg, bogus) {
			t.Errorf("%s：报错没点名那个字段，得到的是 %q —— "+
				"绑定要发生在任何依赖状态的检查之前，否则人会被支到别处去查", ep, e.Msg)
		}
	}
}

// **一份存得下、而用不了的 DNS 服务商配置，比没配更坏。**
//
// 灰度上撞到的：只填了 kind 和凭证、没填域名。保存成功，设置页显示
// 「已配置」，而 DNS 页说「尚未配置服务商」——**两个端点对同一件事说了
// 相反的话，而两句在各自的口径下都对**：
//
//	GET /settings     configured 说的是「凭证在不在」
//	GET /dns/weights  说的是「装配得出客户端吗」（要 kind + domain + credential）
//
// 人看到的是：填完保存成功、徽标变绿、而解析一动不动，
// **没有任何一处说得出缺了什么**。
//
// 校验的判据与装配那一侧**共用 store.MissingFields**。分开写的话，
// 加一个新的必填字段时改了一侧忘了另一侧，症状就是这次这个，而它不报错。
func TestIncompleteDNSProviderIsRejected(t *testing.T) {
	r, _ := newServer(t)
	ck := login(t, r)
	auth := func(req *http.Request) { req.AddCookie(ck) }

	// 一、有 kind 和凭证、没有域名 —— 拒，并且**点名缺的是哪一项**。
	_, e := do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{
			"kind": "cloudflare", "credential_mode": "api_token", "credential": "tok",
		},
	}, auth)
	if e.Code != api.CodeValidation {
		t.Fatalf("缺域名的配置应当以 1002 拒绝，实际 code=%d msg=%q", e.Code, e.Msg)
	}
	if !strings.Contains(string(e.Data), "dns_provider.domain") {
		t.Errorf("要点名缺的是 domain，实际 %s", e.Data)
	}

	// **被拒的那次什么也没存。** 存了一半的话，下一次只填域名就会
	// 「补全」成功，而人不会知道中间那一版曾经存在过。
	_, got := do(t, r, "GET", "/api/v1/settings", nil, auth)
	if strings.Contains(string(got.Data), `"configured":true`) {
		t.Errorf("被拒的配置不该留下痕迹：%s", got.Data)
	}

	// 二、**通用三项齐全，而 Cloudflare 还差它自己要的那两项。**
	//
	// 这一段此前就是「完整的配置」，测试是绿的——**它相信的正是那个 bug**。
	// 灰度上的症状：保存成功、设置页显示已配置，而推权重时 Cloudflare 回
	// `/accounts//load_balancers/pools` 7003「路由不到」。那个双斜杠就是
	// account_id 为空，而那句错误消息不认识我们的字段名。
	_, half := do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{
			"kind": "cloudflare", "domain": "example.com", "sub": "cdn",
			"credential_mode": "api_token", "credential": "tok",
		},
	}, auth)
	if half.Code != api.CodeValidation {
		t.Fatalf("缺 account_id / zone_id 的 Cloudflare 配置应当以 1002 拒绝，"+
			"实际 code=%d msg=%q", half.Code, half.Msg)
	}
	for _, f := range []string{"dns_provider.account_id", "dns_provider.zone_id"} {
		if !strings.Contains(string(half.Data), f) {
			t.Errorf("要点名缺的是 %s，实际 %s", f, half.Data)
		}
	}

	// 三、**反过来：真正完整的配置照常收下。**
	//
	// 没有这一条，一个「无条件拒绝所有 dns_provider」的实现也能让上面全过。
	_, ok := do(t, r, "PUT", "/api/v1/settings", map[string]any{
		"dns_provider": map[string]any{
			"kind": "cloudflare", "domain": "example.com", "sub": "cdn",
			"credential_mode": "api_token", "credential": "tok",
			"account_id": "acc123", "zone_id": "zone123",
		},
	}, auth)
	if ok.Code != api.CodeOK {
		t.Fatalf("完整的配置不该被拒：code=%d msg=%q", ok.Code, ok.Msg)
	}

	// 四、**只改别的设置、根本没碰 dns_provider 时不受影响。**
	//
	// 「一个字段都没填」是「还没开始配」，不是「配错了」——
	// 把它也拒掉的话，一台还没配 DNS 的主控连心跳间隔都改不了。
	_, other := do(t, r, "PUT", "/api/v1/settings",
		map[string]any{"heartbeat_interval_s": 5}, auth)
	if other.Code != api.CodeOK {
		t.Errorf("没碰 DNS 的设置修改不该被 DNS 校验挡住：code=%d msg=%q",
			other.Code, other.Msg)
	}
}
