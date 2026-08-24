package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"

	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/caddytest"
	"github.com/xltxb/edge_caddy/internal/store"
)

// setupSite 建一条路由、接一个节点、下发，返回可以直接 curl 的域名。
func setupSite(t *testing.T, r *rig, domain string) {
	t.Helper()
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")
	r.mustDo("POST", "/routes", map[string]any{
		"domain": domain, "upstream": r.upstream, "block_mode": "404",
	})
	r.deployNow("route:" + domain)
}

// TestIPBlacklistActuallyBlocks 是**真请求进真 Caddy** 的验收。
//
// 黑名单与白名单在渲染出来的配置里只差一个 `not`。少写它，「只拦这些」
// 就变成「只放这些」—— **而配置本身完全合法，Caddy 照收，站点看起来也正常**
// （只要访问者恰好在名单里）。所以这条不看配置长什么样，看请求进不进得来。
func TestIPBlacklistActuallyBlocks(t *testing.T) {
	// **必须走 TCP。** unix socket 上 remote_ip 匹配不到任何东西
	// ——实测过：白名单放行回环，走 unix socket 时回 404。
	// 用默认的 rig 的话，这条测试验的是「什么都匹配不到」，而它照样能绿。
	r := newRig(t, caddytest.EdgeTCP())
	setupSite(t, r, "black.example.com")

	// 先确认没规则时通得过 —— **没有这一步，下面的 404 可能是别的原因**。
	if code, _ := r.curlVia("black.example.com"); code != 200 {
		t.Fatalf("装置坏了：还没加规则就通不过（%d）", code)
	}

	// 把测试自己的来源 IP 拉黑。e2e 里 Caddy 与测试同机，来源是回环。
	r.mustDo("PUT", "/rules/bl", map[string]any{
		"name": "拉黑回环", "type": "ip_blacklist", "enabled": true,
		"apply_to": []string{"black.example.com"},
		"spec":     map[string]any{"ips": []string{"127.0.0.0/8", "::1/128"}},
	})
	r.deployNow("rule:bl")

	if code, _ := r.curlVia("black.example.com"); code != 404 {
		t.Errorf("在黑名单里的来源应当被拦（按 block_mode 回 404），实际 %d "+
			"—— 少一个 not 的话黑名单会变成白名单，而配置完全合法", code)
	}
}

// TestIPBlacklistLetsOthersThrough 是上一条的反面。
//
// 没有它，一个「无条件拦」的实现也能让上面全绿 —— 而那会把站点整个封掉。
func TestIPBlacklistLetsOthersThrough(t *testing.T) {
	// **必须走 TCP。** unix socket 上 remote_ip 匹配不到任何东西
	// ——实测过：白名单放行回环，走 unix socket 时回 404。
	// 用默认的 rig 的话，这条测试验的是「什么都匹配不到」，而它照样能绿。
	r := newRig(t, caddytest.EdgeTCP())
	setupSite(t, r, "black2.example.com")

	// 拉黑一个**不是**测试来源的网段。
	r.mustDo("PUT", "/rules/bl2", map[string]any{
		"name": "拉黑别人", "type": "ip_blacklist", "enabled": true,
		"apply_to": []string{"black2.example.com"},
		"spec":     map[string]any{"ips": []string{"203.0.113.0/24"}},
	})
	r.deployNow("rule:bl2")

	if code, _ := r.curlVia("black2.example.com"); code != 200 {
		t.Errorf("不在黑名单里的来源应当照常通过，实际 %d —— "+
			"黑名单写成了白名单的话就是这个症状", code)
	}
}

// TestRequestFilterBlocksByUserAgent 验按请求特征拦。
func TestRequestFilterBlocksByUserAgent(t *testing.T) {
	r := newRig(t)
	setupSite(t, r, "wafish.example.com")

	r.mustDo("PUT", "/rules/ua", map[string]any{
		"name": "挡扫描器", "type": "request_filter", "enabled": true,
		"apply_to": []string{"wafish.example.com"},
		"spec": map[string]any{"filters": []map[string]any{
			{"field": "user_agent", "op": "contains", "value": "sqlmap"},
			{"field": "path", "op": "prefix", "value": "/.git"},
		}},
	})
	r.deployNow("rule:ua")

	// 命中 UA。
	if code, _ := r.caddy.Get("wafish.example.com", "/",
		map[string]string{"User-Agent": "sqlmap/1.7"}); code != 404 {
		t.Errorf("命中 UA 特征应当被拦，实际 %d", code)
	}
	// 命中路径。
	if code, _ := r.caddy.Get("wafish.example.com", "/.git/config", nil); code != 404 {
		t.Errorf("命中路径特征应当被拦，实际 %d", code)
	}
	// **两条特征之间是「或」**：只命中一条也要拦，上面两个各只命中一条。
	// 而正常请求两条都不命中，要放行 —— 没有这一条，
	// 一个把多个条件写成「且」的实现会在上面两个用例上就红，
	// 而一个「无条件拦」的实现只有这一条能抓到。
	if code, _ := r.caddy.Get("wafish.example.com", "/",
		map[string]string{"User-Agent": "Mozilla/5.0"}); code != 200 {
		t.Errorf("两条特征都不命中的正常请求应当放行，实际 %d", code)
	}
}

// TestRequestFilterRejectsBadRegexBeforeDeploy 钉的是**坏正则不能下发**。
//
// 一条写错的正则原样下发到节点上，Caddy 会**拒绝整份配置** ——
// 症状是「所有站点一起下发失败」，而根因是某一条规则里的一个括号。
func TestRequestFilterRejectsBadRegexBeforeDeploy(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "re.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	_, e := r.do("PUT", "/rules/badre", map[string]any{
		"name": "坏正则", "type": "request_filter", "enabled": true,
		"apply_to": []string{"re.example.com"},
		"spec": map[string]any{"filters": []map[string]any{
			{"field": "path", "op": "regex", "value": "(unclosed"},
		}},
	})
	if e.Code != api.CodeValidation {
		t.Fatalf("编译不过的正则应当以 1002 拒绝，实际 code=%d msg=%q", e.Code, e.Msg)
	}
}

// TestEmptyBlacklistIsRejected：空黑名单谁也拦不到，而它在界面上显示为启用。
//
// 与空白名单的理由相反（那个会拦下所有人），所以两句话不同 ——
// **一句「不能为空」对两种类型说的是两件事**。
func TestEmptyBlacklistIsRejected(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "eb.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	_, e := r.do("PUT", "/rules/eb", map[string]any{
		"name": "空黑名单", "type": "ip_blacklist", "enabled": true,
		"apply_to": []string{"eb.example.com"},
		"spec":     map[string]any{"ips": []string{}},
	})
	if e.Code != api.CodeValidation {
		t.Fatalf("空黑名单应当以 1002 拒绝，实际 code=%d msg=%q", e.Code, e.Msg)
	}
}

// TestRateLimitActuallyThrottles 是限流的真验收：**真请求进真 Caddy**。
//
// 走的是 Agent 校验端点（ADR-0003 那条委托）——官方 Caddy 没有限流模块。
//
// 这条同时钉住 429：`handle_response` 只匹配 2xx，其余状态码原样回给客户端。
// **那是一条关于 Caddy 行为的断言**，靠这条测试守着——
// 它要是变了，症状是限流回 502 而不是 429，而客户端的重试逻辑会因此放弃。
func TestRateLimitActuallyThrottles(t *testing.T) {
	r := newRig(t, caddytest.EdgeTCP())
	setupSite(t, r, "rl.example.com")

	r.mustDo("PUT", "/rules/rl", map[string]any{
		"name": "限流", "type": "rate_limit", "enabled": true,
		"apply_to": []string{"rl.example.com"},
		"spec":     map[string]any{"requests": 3, "window_s": 60, "rate_key": "ip"},
	})
	r.deployNow("rule:rl")

	// 桶容量 3：头三个放行。
	for i := 0; i < 3; i++ {
		if code, _ := r.curlVia("rl.example.com"); code != 200 {
			t.Fatalf("第 %d 个请求就被拦了（%d）—— 桶容量是 3", i+1, code)
		}
	}
	// 第四个拦下，而且要是 429 不是 403/502。
	code, _ := r.curlVia("rl.example.com")
	if code != 429 {
		t.Fatalf("超出额度应当回 429，实际 %d —— "+
			"403 会被客户端当成鉴权失败而放弃重试，502 会被当成服务挂了", code)
	}
}

// TestRateLimitIgnoresForgedForwardedFor 钉的是**计数的键攻击者拿不到**。
//
// 每次换一个伪造的来源头，如果按它计数就永远打不满 ——
// 那时限流的键由攻击者控制，等于没有限流。
//
// # 它区分不了两个来源，这一点是实测出来的
//
// 把实现改成读 X-Forwarded-For，**这条照样绿**。原因：默认配置下
// Caddy 把客户端发来的 XFF 整个替换成真实来源（实测 XFF=127.0.0.1，
// 客户端发的 9.9.9.9 消失了），所以两个头在这里都是安全的。
//
// **所以它守的是「伪造的头不会开出新桶」，不是「我们读对了那个头」。**
// 后者由 render.go 里那段注释解释理由（XFF 的含义取决于 trusted_proxies，
// 而那是会被改的），而**没有任何测试守着它** —— 写出来是为了让下一个人
// 知道这一层是空的，而不是以为这条绿着就都验过了。
func TestRateLimitIgnoresForgedForwardedFor(t *testing.T) {
	r := newRig(t, caddytest.EdgeTCP())
	setupSite(t, r, "rlx.example.com")

	r.mustDo("PUT", "/rules/rlx", map[string]any{
		"name": "限流", "type": "rate_limit", "enabled": true,
		"apply_to": []string{"rlx.example.com"},
		"spec":     map[string]any{"requests": 2, "window_s": 60},
	})
	r.deployNow("rule:rlx")

	// 每次换一个伪造的 XFF —— 如果按它计数，就永远打不满。
	for i, fake := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"} {
		code, _ := r.caddy.Get("rlx.example.com", "/",
			map[string]string{"X-Forwarded-For": fake,
				"X-Edge-Client-IP": fake}) // 连这个头也一起伪造试试
		if i < 2 && code != 200 {
			t.Fatalf("第 %d 个就被拦（%d）", i+1, code)
		}
		if i >= 2 && code != 429 {
			t.Errorf("第 %d 个换了假 IP 就绕过了限流（%d）—— "+
				"计数的键被攻击者控制，等于没有限流", i+1, code)
		}
	}
}

// TestGeoRuleWithoutDBLetsTrafficThrough 钉的是**库缺失时放行，不是全封**。
//
// 这是地域这一层最要紧的决定。拒绝的话，库还没下发到的那段时间里
// （新装的节点、下发失败、文件被删），每个受地域规则保护的域名
// **对所有人都是 403** —— 而配置看起来完全正常。
//
// 这与校验端点整体的 fail-closed 不同，而区别是有理由的：
// 服务密钥验不过说明**这个请求**没有凭据；没有库说明**我们**没准备好。
// 把我们的问题变成所有访问者的 403，是把一次运维疏忽放大成一次全站故障。
//
// 代价是**库没到之前这条规则形同虚设** —— 所以它必须被看见：
// Agent 会把这件事记进日志，节点页上看得到。这条测试连那条日志一起钉。
func TestGeoRuleWithoutDBLetsTrafficThrough(t *testing.T) {
	r := newRig(t, caddytest.EdgeTCP())
	setupSite(t, r, "geo.example.com")

	r.mustDo("PUT", "/rules/geo", map[string]any{
		"name": "封禁", "type": "geo_block", "enabled": true,
		"apply_to": []string{"geo.example.com"},
		"spec":     map[string]any{"geo_mode": "block", "geo_countries": []string{"CN"}},
	})
	r.deployNow("rule:geo")

	if code, _ := r.curlVia("geo.example.com"); code != 200 {
		t.Fatalf("本机还没有 GeoIP 库时应当放行，实际 %d —— "+
			"拒绝的话，库没下发到的那段时间里这个域名对所有人都是 403", code)
	}

	// **它必须被看见。** 悄悄放行等于一条不存在的规则，
	// 而界面上它显示为启用。
	if !r.waitForNodeLog("node-hk-01", "GeoIP") {
		t.Error("放行了却没说 —— 一条悄悄失效的安全规则比没有规则更坏")
	}
}

// TestGeoDBReachesNodeAndTakesEffect 是地域这条链路的真验收。
//
// 从主控库里放一份 mmdb，到节点心跳报出旧哈希、主控推送、节点落盘热加载、
// 地域规则真的按它拦人 —— **每一环都是真的**，没有打桩。
//
// 判据是「请求被拦了」，不是「推送成功了」：推送成功只说明字节到了，
// 而这一整条链路里任何一环把库放错地方、加载失败、或者哈希没更新，
// 表现都是「推送成功而规则不生效」。
func TestGeoDBReachesNodeAndTakesEffect(t *testing.T) {
	r := newRig(t, caddytest.EdgeTCP())

	// 主控先有一份库：把回环判成 CN。
	mmdb := readFixtureMMDB(t)
	sum := sha256.Sum256(mmdb)
	if err := r.store.PutGeoDB(context.Background(), store.GeoDB{
		MMDB: mmdb, SHA256: hex.EncodeToString(sum[:]), Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}

	setupSite(t, r, "geo2.example.com")
	r.mustDo("PUT", "/rules/geo2", map[string]any{
		"name": "封禁 CN", "type": "geo_block", "enabled": true,
		"apply_to": []string{"geo2.example.com"},
		"spec":     map[string]any{"geo_mode": "block", "geo_countries": []string{"CN"}},
	})
	r.deployNow("rule:geo2")

	// 等库到达并生效：**判据是行为，不是日志**。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if code, _ := r.curlVia("geo2.example.com"); code == 403 {
			return // 成了
		}
		time.Sleep(300 * time.Millisecond)
	}
	code, _ := r.curlVia("geo2.example.com")
	t.Fatalf("库推到节点之后回环该被判成 CN 并拦下，实际 %d —— "+
		"这一整条链路里任何一环（推送、落盘、热加载、哈希更新）断了，"+
		"表现都是「推送成功而规则不生效」", code)
}

// readFixtureMMDB 造一份把**回环判成 CN** 的最小 mmdb。
//
// 回环是保留网络，mmdbwriter 默认拒绝往里插 —— 要显式开
// IncludeReservedNetworks。真库里当然不会有这一段，
// 而 e2e 里请求只能从回环发出，所以这是唯一能验到「按国家拦」的办法。
func readFixtureMMDB(t *testing.T) []byte {
	t.Helper()
	w, err := mmdbwriter.New(mmdbwriter.Options{
		DatabaseType:            "GeoLite2-Country",
		RecordSize:              24,
		IncludeReservedNetworks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, cidr := range []string{"127.0.0.0/8", "::1/128"} {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Insert(n, mmdbtype.Map{
			"country": mmdbtype.Map{"iso_code": mmdbtype.String("CN")},
		}); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if _, err := w.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestNodeReportsGeoDBHash 钉的是**「哪些节点还没有库」看得见**。
//
// 不看得见的话，「地域规则形同虚设」这件事只出现在节点日志里 ——
// 而那要人主动去翻。一条界面上显示为启用、而实际不生效的安全规则，
// 比没有这条规则更坏。
//
// 它同时守住哈希更新那一环：节点收下库却不更新哈希的话，
// 主控会**每次心跳都重推一份几 MB 的库**，而地域规则照常生效
// —— 于是那个 bug 从行为上完全看不出来。
func TestNodeReportsGeoDBHash(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	// 主控还没有库：这一列该是 null，**不是 false**。
	// 没在用地域功能的系统里，每台节点都标红是在报告一个不存在的问题。
	if got := geoOKOf(t, r); got != nil {
		t.Errorf("主控没有库时 geo_db_ok 该是 null，实际 %v", *got)
	}

	mmdb := readFixtureMMDB(t)
	sum := sha256.Sum256(mmdb)
	if err := r.store.PutGeoDB(context.Background(), store.GeoDB{
		MMDB: mmdb, SHA256: hex.EncodeToString(sum[:]), Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}

	// 主控有了库而节点还没收到：false。收到之后转 true。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if got := geoOKOf(t, r); got != nil && *got {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	got := geoOKOf(t, r)
	t.Fatalf("库推到节点之后 geo_db_ok 该转 true，实际 %s —— "+
		"节点收下库却不更新哈希的话，主控会每次心跳都重推一份几 MB 的库，"+
		"而地域规则照常生效，这个 bug 从行为上完全看不出来", boolPtrStr(got))
}

func geoOKOf(t *testing.T, r *rig) *bool {
	t.Helper()
	e := r.mustDo("GET", "/nodes", nil)
	var d struct {
		Items []struct {
			GeoDBOK *bool `json:"geo_db_ok"`
		} `json:"items"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 1 {
		t.Fatalf("装置坏了：该有 1 个节点，实际 %d", len(d.Items))
	}
	return d.Items[0].GeoDBOK
}

// boolPtrStr 把 *bool 写成人读得懂的。**直接 %v 打的是指针地址** ——
// 一个测试失败信息里印出 0x3a0ee10c4d60，等于没有这条信息。
func boolPtrStr(b *bool) string {
	if b == nil {
		return "null"
	}
	if *b {
		return "true"
	}
	return "false"
}
