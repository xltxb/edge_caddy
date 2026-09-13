package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
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

// TestFilterFieldsAreServedFromTheSameTableAsValidation 钉的是**下拉与校验同源**。
//
// 界面按报出来的这张表渲染 op 下拉。抄一份的话，加第八种 field 时两边分叉：
// 界面给出一个后端会拒的选项（人配完被拒，还算看得见），
// 或者**藏起一个后端接受的**——而那从界面上完全看不出来。
//
// 判法是逆着来：对报出来的每一对 (field, op)，构造一条规则去存 ——
// 报了而存不进去，说明表和校验对不上账。
func TestFilterFieldsAreServedFromTheSameTableAsValidation(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "ff.example.com", "upstream": r.upstream, "block_mode": "abort",
	})

	e := r.mustDo("GET", "/rules", nil)
	var d struct {
		FilterFields map[string][]string `json:"filter_fields"`
		NeedName     map[string]bool     `json:"filter_fields_need_name"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.FilterFields) < 3 {
		t.Fatalf("装置坏了：只报了 %d 个 field，下面的循环没有意义", len(d.FilterFields))
	}

	checked := 0
	for field, ops := range d.FilterFields {
		for _, op := range ops {
			f := map[string]any{"field": field, "op": op, "value": "x"}
			if d.NeedName[field] {
				f["name"] = "X-Test"
			}
			// field 名带下划线（user_agent），而资源 id 不收下划线
			// （契约 §0.7）—— 这条 id 是测试自己造的，不是被测对象。
			id := "ff-" + strings.ReplaceAll(field, "_", "-") + "-" + op
			_, res := r.do("PUT", "/rules/"+id, map[string]any{
				"name": "特征", "type": "request_filter", "enabled": true,
				"apply_to": []string{"ff.example.com"},
				"spec":     map[string]any{"filters": []map[string]any{f}},
			})
			if res.Code != api.CodeOK {
				t.Errorf("报了 %s/%s 而存不进去（code=%d msg=%q）—— "+
					"界面照这张表渲染下拉，人会配一个必然被拒的组合",
					field, op, res.Code, res.Msg)
			}
			checked++
		}
	}
	if checked < 10 {
		t.Fatalf("只试了 %d 对 —— 这条测试此刻几乎什么也没验", checked)
	}

	// 反面：表里没报的组合要被拒。没有这一条，一张「什么都报」的表
	// 也能让上面全绿，而那等于没有收窄。
	_, res := r.do("PUT", "/rules/ff-bad", map[string]any{
		"name": "特征", "type": "request_filter", "enabled": true,
		"apply_to": []string{"ff.example.com"},
		"spec": map[string]any{"filters": []map[string]any{
			{"field": "query", "name": "q", "op": "contains", "value": "x"},
		}},
	})
	if res.Code != api.CodeValidation {
		t.Errorf("query 只报了 equals，contains 该被拒，实际 code=%d", res.Code)
	}
}

// TestDisabledRuleCanHoldAnEmptySpec 钉的是**一条契约里写着、而没人守着的行为**。
//
// 校验只跑在**启用且已绑定**的规则上——那是刻意的：人本来就是
// 「先建规则、再慢慢配」的顺序，一条还没填完的规则不该挡住全站的下发。
//
// 代价是 `spec` 可以是 `{}`：**连那个类型本该有的键都没有**。
// 照着「这个类型一定有这个键」写的 `spec.ips.map(...)` 会崩在它上面。
//
// 我是用一个一次性探针发现这一档的，跑完就删了 —— 而那正是
// 「用完就丢的检查，等于只在写它的那一刻生效过一次」。
// 契约里写了这件事，而**写在契约里的东西不会自己保持为真**：
// 哪天有人把校验改成「停用的也查」，那段契约会在没有任何东西变红的情况下过期。
func TestDisabledRuleCanHoldAnEmptySpec(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "draft.example.com", "upstream": r.upstream, "block_mode": "abort",
	})

	// 一、停用 + 空 spec：存得进去。
	_, e := r.do("PUT", "/rules/draft", map[string]any{
		"name": "还没填完", "type": "ip_whitelist", "enabled": false,
		"apply_to": []string{"draft.example.com"},
		"spec":     map[string]any{"ips": []string{}},
	})
	if e.Code != api.CodeOK {
		t.Fatalf("停用的规则该存得进去（人先建再慢慢配），实际 code=%d msg=%q",
			e.Code, e.Msg)
	}

	// 二、读回来时 spec 里**连 ips 这个键都没有**。
	//
	// 判据是「键在不在」，不是「值是什么」：解到结构体的话，
	// 键不存在和空数组都给出 nil —— 而下游要防的正是前者。
	list := r.mustDo("GET", "/rules", nil)
	var d struct {
		Items []struct {
			ID   string                     `json:"id"`
			Spec map[string]json.RawMessage `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 1 {
		t.Fatalf("装置坏了：该有 1 条规则，实际 %d", len(d.Items))
	}
	if _, ok := d.Items[0].Spec["ips"]; ok {
		t.Errorf("契约说这一档的 spec 是 {} —— 而它现在有 ips 这个键。"+
			"要么是行为变了（那契约那段要跟着改），"+
			"要么是校验开始查停用的规则了：%v", d.Items[0].Spec)
	}

	// 三、**反面：启用它就要被拒。** 没有这一条，一个「从不校验」的实现
	// 也能让上面全绿，而那意味着空白名单能被下发到节点上。
	_, bad := r.do("PUT", "/rules/draft", map[string]any{
		"name": "还没填完", "type": "ip_whitelist", "enabled": true,
		"apply_to": []string{"draft.example.com"},
		"spec":     map[string]any{"ips": []string{}},
	})
	if bad.Code != api.CodeValidation {
		t.Errorf("启用一条空白名单该被拒（它会拦下所有访问），实际 code=%d", bad.Code)
	}
}

// TestDeployRefusedWhenNodeCannotUnderstandRule 钉的是**升级期的那道门**。
//
// 实测出来的：旧 Agent 收到一条它不认识的规则类型（rate_limit / geo_block）时，
// 校验端点走 default 分支回 403 —— 那个域名的**第一个请求就被拒**。
// 不是降级，是整站对所有人关闭，而配置看起来完全正常。
//
// 校验端点是 fail-closed 的（ADR-0003），那对「这个请求没有凭据」是对的；
// 而「这个节点不认识这条规则」是另一回事 —— 把我们的版本落后变成所有
// 访问者的 403，是把一次升级疏忽放大成一次全站故障。
//
// 拦在下发这一步而不是让节点拒：节点拒的话人看到的是「下发失败」，
// 而**已经应用了的那些节点已经是新配置** —— 一半新一半旧，最难查的一种状态。
func TestDeployRefusedWhenNodeCannotUnderstandRule(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "up.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	r.deployNow("route:up.example.com")

	r.mustDo("PUT", "/rules/up", map[string]any{
		"name": "限流", "type": "rate_limit", "enabled": true,
		"apply_to": []string{"up.example.com"},
		"spec":     map[string]any{"requests": 10, "window_s": 60},
	})

	// 一、当前这个 Agent 认得 rate_limit，下发该通过。
	//
	// **这一条是承重的**：没有它，一个「无条件拒绝」的实现也能让下面那条全绿，
	// 而那意味着这四种规则永远下发不出去。
	if _, e := r.do("POST", "/deploys", map[string]any{
		"res_keys": []string{"rule:up"},
	}); e.Code != api.CodeOK {
		t.Fatalf("当前 Agent 认得 rate_limit，不该被拦：code=%d msg=%q", e.Code, e.Msg)
	}

	// 二、把库里那台节点的能力清空，模拟一台旧 Agent。
	//
	// **空表示旧 Agent**（那个字段是后加的），不是「一种都不认得」——
	// 主控按「只认得 service_secret / jwt_bearer」处理。
	if _, err := r.store.Pool.Exec(context.Background(),
		`UPDATE edge_nodes SET verify_kinds = '{}' WHERE id = 'node-hk-01'`); err != nil {
		t.Fatal(err)
	}

	_, e := r.do("POST", "/deploys", map[string]any{"res_keys": []string{"rule:up"}})
	if e.Code != api.CodeValidation {
		t.Fatalf("节点不认识这条规则时该整体拒绝，实际 code=%d msg=%q", e.Code, e.Msg)
	}
	for _, want := range []string{"node-hk-01", "403", "update"} {
		if !strings.Contains(string(e.Data), want) {
			t.Errorf("报错里没有 %q —— 要说清是哪台机器、后果是什么、怎么修：%s",
				want, e.Data)
		}
	}
}

// TestLegacyRulesStillDeployToOldAgents 是反面。
//
// 那道门只该拦**走校验端点**的新类型。`ip_blacklist` / `request_filter`
// 是 Caddy 原生匹配器渲染出来的 —— 旧 Agent 照样应用得了，它只是把一份
// Caddy 配置贴上去，不需要认识里面的任何东西。
//
// 没有这一条，一道「凡是新类型都拦」的门会把两个本来兼容的功能一起挡住。
func TestLegacyRulesStillDeployToOldAgents(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "lg.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	if _, err := r.store.Pool.Exec(context.Background(),
		`UPDATE edge_nodes SET verify_kinds = '{}' WHERE id = 'node-hk-01'`); err != nil {
		t.Fatal(err)
	}

	r.mustDo("PUT", "/rules/lg", map[string]any{
		"name": "黑名单", "type": "ip_blacklist", "enabled": true,
		"apply_to": []string{"lg.example.com"},
		"spec":     map[string]any{"ips": []string{"198.51.100.0/24"}},
	})
	if _, e := r.do("POST", "/deploys", map[string]any{
		"res_keys": []string{"route:lg.example.com", "rule:lg"},
	}); e.Code != api.CodeOK {
		t.Fatalf("ip_blacklist 走 Caddy 原生匹配器，旧 Agent 照样应用得了，"+
			"不该被拦：code=%d msg=%q", e.Code, e.Msg)
	}
}

// TestIncompleteRulesAreVisibleInTheList 钉的是**列表上就看得见，不必等到下发**。
//
// 灰度上撞到的：一条 `window_s` 为 0 的限流规则存进了库，
// 而它是在**下发那一刻**才被拒的 —— 那条规则可能是几天前建的。
// **校验拦住的地方，离出错的地方隔了很远。**
//
// 它进得来是因为校验只跑在「启用且已绑定」的规则上（那是刻意的：
// 半成品不该挡住全站下发）。而「这条规则完不完整」是另一个问题 ——
// 它对停用的规则**同样成立**。
//
// 判据是**「下发会被拒的，列表上已经标出来了」**，
// 不是「列表上有个字段」——后者一个恒为空的实现也能满足。
func TestIncompleteRulesAreVisibleInTheList(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "inc.example.com", "upstream": r.upstream, "block_mode": "abort",
	})

	// 一条停用的半成品：window_s 缺了。**停用所以校验跳过，存得进去。**
	r.mustDo("PUT", "/rules/half", map[string]any{
		"name": "半成品限流", "type": "rate_limit", "enabled": false,
		"apply_to": []string{"inc.example.com"},
		"spec":     map[string]any{"requests": 100, "rate_key": "ip"},
	})
	// 一条完整的，用来验反面。
	r.mustDo("PUT", "/rules/whole", map[string]any{
		"name": "完整的", "type": "ip_blacklist", "enabled": true,
		"apply_to": []string{"inc.example.com"},
		"spec":     map[string]any{"ips": []string{"198.51.100.0/24"}},
	})

	e := r.mustDo("GET", "/rules", nil)
	var d struct {
		Incomplete map[string][]struct {
			Field  string `json:"field"`
			Reason string `json:"reason"`
		} `json:"incomplete"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}

	// 半成品要被标出来，**并且点名是哪个字段**。
	if len(d.Incomplete["half"]) == 0 {
		t.Fatalf("那条 window_s 缺了的规则没被标出来 —— "+
			"人要等到几天后下发时才知道：%s", e.Data)
	}
	var sawWindow bool
	for _, i := range d.Incomplete["half"] {
		if i.Field == "spec.window_s" {
			sawWindow = true
		}
	}
	if !sawWindow {
		t.Errorf("要点名是哪个字段，否则人不知道去填哪儿：%+v", d.Incomplete["half"])
	}

	// **完整的那条要是空数组，不是 null。**
	// null 的意思是「这次算不出来」（§0.4），而它算过了。
	whole, ok := d.Incomplete["whole"]
	if !ok {
		t.Fatal("完整的规则也该有这一项（空数组），而不是整个不出现")
	}
	if len(whole) != 0 {
		t.Errorf("完整的规则被标成不完整了：%+v", whole)
	}
}

// TestUnboundRuleIsIncompleteToo：配得再全，不绑域名也不会生效。
//
// 它不是 spec 的问题，所以单列一条 —— 而合成「spec 有问题」的话，
// 人会去检查那些填得好好的字段。
func TestUnboundRuleIsIncompleteToo(t *testing.T) {
	r := newRig(t)
	r.mustDo("PUT", "/rules/free", map[string]any{
		"name": "没绑域名", "type": "ip_blacklist", "enabled": true,
		"apply_to": []string{},
		"spec":     map[string]any{"ips": []string{"198.51.100.0/24"}},
	})
	e := r.mustDo("GET", "/rules", nil)
	var d struct {
		Incomplete map[string][]struct {
			Field string `json:"field"`
		} `json:"incomplete"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	var sawApplyTo bool
	for _, i := range d.Incomplete["free"] {
		if i.Field == "apply_to" {
			sawApplyTo = true
		}
	}
	if !sawApplyTo {
		t.Errorf("没绑域名的规则该被标出来（它不会对任何请求生效）：%+v",
			d.Incomplete["free"])
	}
}

// TestConfiguredSecretIsNotReportedAsIncomplete：已配好密钥的不算缺。
//
// 列表不解密（凭证不回显），而判据是 SecretConfigured 说的「库里有没有」——
// 不这么做的话，**每条配好密钥的服务密钥规则都会被标成不完整**，
// 而那种恒为红的标记，人两天就学会忽略了。
func TestConfiguredSecretIsNotReportedAsIncomplete(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "sec.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	r.mustDo("PUT", "/rules/svc", map[string]any{
		"name": "服务密钥", "type": "service_secret", "enabled": true,
		"apply_to": []string{"sec.example.com"},
		"spec":     map[string]any{"header": "X-Service-Key", "algo": "hmac-sha256", "ttl_s": 300},
		"secret":   "s3cr3t",
	})
	e := r.mustDo("GET", "/rules", nil)
	var d struct {
		Incomplete map[string][]struct {
			Field  string `json:"field"`
			Reason string `json:"reason"`
		} `json:"incomplete"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Incomplete["svc"]) != 0 {
		t.Errorf("配好密钥的规则被标成不完整了 —— 恒为红的标记人两天就学会忽略：%+v",
			d.Incomplete["svc"])
	}
}

// TestBlockedCountReflectsRealBlocks 钉的是**拦了多少，界面上看得见**。
//
// 没有它的话，**一个正在挡住 CC 的系统和一个规则根本没生效的系统，
// 在界面上长得一模一样** —— 请求数会涨，而人分不出「来了很多正常流量」
// 和「正在被打而我们挡住了」。
//
// 判据是「真的拦了之后那个数真的涨」，不是「有这么个字段」——
// 后者一个恒为 0 的实现也能满足。
func TestBlockedCountReflectsRealBlocks(t *testing.T) {
	r := newRig(t, caddytest.EdgeTCP())
	setupSite(t, r, "bc.example.com")

	// 桶容量 2、窗口很长 —— 打 6 个，后面 4 个必被拦。
	r.mustDo("PUT", "/rules/bc", map[string]any{
		"name": "限流", "type": "rate_limit", "enabled": true,
		"apply_to": []string{"bc.example.com"},
		"spec":     map[string]any{"requests": 2, "window_s": 600},
	})
	r.deployNow("rule:bc")

	// **先等基线建立，再制造拦截。**
	//
	// 这个数是从累计值算差值来的，而差值要两次心跳才算得出。
	// 第一次心跳之前发生的拦截会被算进基线里 —— 那是个真实性质
	// （Agent 刚起来那几秒里的拦截数不到），而不是缺陷：
	// 累计值本身在 Agent 重启时也会归零。
	//
	// 第一版没等，于是 6 个请求全在第一次心跳之前打完了，
	// 基线直接建在 4 上，差值恒为 0 —— **测试红了，而红的是测试不是代码**。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && blockedOf(t, r) == nil {
		time.Sleep(200 * time.Millisecond)
	}
	if blockedOf(t, r) == nil {
		t.Fatal("等不到第一次心跳，基线建不起来")
	}

	blocked := 0
	for i := 0; i < 6; i++ {
		if code, _ := r.curlVia("bc.example.com"); code == 429 {
			blocked++
		}
	}
	if blocked == 0 {
		t.Fatalf("装置坏了：打了 6 个，一个都没被拦（桶容量是 2）")
	}

	// 等下一次心跳把这一段的差值报上来。
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if got := blockedOf(t, r); got != nil && *got >= uint64(blocked) {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	got := blockedOf(t, r)
	shown := "null"
	if got != nil {
		shown = fmt.Sprint(*got)
	}
	t.Fatalf("真的拦了 %d 个，而 blocked_1h 是 %s —— "+
		"这个数是「此刻在不在被打」唯一的答案，它不动等于那个问题没人回答",
		blocked, shown)
}

// TestBlockedCountIsNullBeforeAnyBaseline：还算不出来时是 null，不是 0。
//
// 节点刚接入时还没有可比的两次心跳。**回 0 会被读成「一个都没拦」，
// 而这个字段要回答的恰恰是「此刻在不在被打」—— `0` 正是「没被打」的样子。**
// 与 reconnects_1h 同一条理由。
func TestBlockedCountIsNullBeforeAnyBaseline(t *testing.T) {
	r := newRig(t)
	// **直接建一行节点记录，不接 Agent。**
	//
	// 签发 Token 不建记录（记录在接入那一刻才建），而接了 Agent 就会有心跳
	// —— 那就造不出「有这个节点、而它从没报过」这一档了。
	if _, err := r.store.Pool.Exec(context.Background(),
		`INSERT INTO edge_nodes (id, city, vendor, line, public_ip, status)
		 VALUES ('node-hk-01','香港','DMIT','CN2','203.0.113.7','down')`); err != nil {
		t.Fatal(err)
	}
	if got := blockedOf(t, r); got != nil {
		t.Errorf("还没有任何心跳时该是 null，实际 %d —— "+
			"0 会被读成「一个都没拦」，而真相是「还不知道」", *got)
	}
}

func blockedOf(t *testing.T, r *rig) *uint64 {
	t.Helper()
	e := r.mustDo("GET", "/nodes", nil)
	var d struct {
		Items []struct {
			Blocked *uint64 `json:"blocked_1h"`
		} `json:"items"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 1 {
		t.Fatalf("装置坏了：该有 1 个节点，实际 %d", len(d.Items))
	}
	return d.Items[0].Blocked
}

// TestBlockedCountCountsRuleDeniesToo：访问规则拦下的也要数到。
//
// 限流走校验端点，Agent 自己数；而 IP 黑名单 / 请求特征是 Caddy 原生匹配器拦的
// —— 那一半从 Caddy 的指标里读（`handler="static_response"`，
// 那个 handler **只有拦截在用**）。
//
// 两个来源缺一个的话，这个数会漏掉一整类拦截，而**它看起来只是「小一点」**。
func TestBlockedCountCountsRuleDeniesToo(t *testing.T) {
	r := newRig(t, caddytest.EdgeTCP())
	setupSite(t, r, "bd.example.com") // block_mode 是 404

	r.mustDo("PUT", "/rules/bd", map[string]any{
		"name": "拉黑回环", "type": "ip_blacklist", "enabled": true,
		"apply_to": []string{"bd.example.com"},
		"spec":     map[string]any{"ips": []string{"127.0.0.0/8", "::1/128"}},
	})
	r.deployNow("rule:bd")

	waitBaseline(t, r)

	for i := 0; i < 3; i++ {
		if code, _ := r.curlVia("bd.example.com"); code != 404 {
			t.Fatalf("装置坏了：第 %d 个没被拦（%d）", i+1, code)
		}
	}

	if !waitBlockedAtLeast(t, r, 3) {
		got := blockedOf(t, r)
		t.Fatalf("黑名单拦了 3 个，而 blocked_1h 是 %s —— "+
			"限流那一半数得到、这一半数不到的话，这个数会漏掉一整类拦截，"+
			"而它看起来只是「小一点」", fmtBlocked(got))
	}
}

// TestUpstreamErrorsAreNotCountedAsBlocked 是承重的反面。
//
// **一个正常网站有大量 404**，而它们不是被我们拦下的。
// 按状态码算的话（第一版就是），这个数会被噪音淹没 ——
// 而它要回答的是「此刻在不在被打」，**一个平时就很大的数回答不了那个问题**。
func TestUpstreamErrorsAreNotCountedAsBlocked(t *testing.T) {
	r := newRig(t, caddytest.EdgeTCP())
	setupSite(t, r, "ue.example.com")
	waitBaseline(t, r)

	// 上游自己回 404，没有任何规则参与。
	for i := 0; i < 5; i++ {
		if code, _ := r.caddy.Get("ue.example.com", "/missing", nil); code != 404 {
			t.Fatalf("装置坏了：上游该回 404，实际 %d", code)
		}
	}

	// 给两次心跳的时间，确认它**没有**涨。
	time.Sleep(2 * time.Second)
	got := blockedOf(t, r)
	if got != nil && *got > 0 {
		t.Errorf("上游自己的 404 被算成「被拦」了（%d）—— "+
			"一个正常网站有大量 404，这个数会被噪音淹没", *got)
	}
}

// waitBaseline 等第一次心跳建立基线。差值要两次心跳才算得出。
func waitBaseline(t *testing.T, r *rig) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && blockedOf(t, r) == nil {
		time.Sleep(200 * time.Millisecond)
	}
	if blockedOf(t, r) == nil {
		t.Fatal("等不到第一次心跳，基线建不起来")
	}
}

func waitBlockedAtLeast(t *testing.T, r *rig, n uint64) bool {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if got := blockedOf(t, r); got != nil && *got >= n {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

func fmtBlocked(b *uint64) string {
	if b == nil {
		return "null"
	}
	return fmt.Sprint(*b)
}

// TestIPv4OnlyWhitelistBlocksIPv6Clients 钉的是**跨地址族的那个坑**。
//
// 有人把白名单写成 `0.0.0.0/0`，想的是「放行所有人」——
// 而 `0.0.0.0/0` 只覆盖 IPv4。一个 IPv6 访客不在这个范围里，
// `not` 于是成立，请求被拦。
//
// **语义上这是对的**（白名单里没有 v6 段，就该拦 v6），所以不该在代码里
// 拿掉它。要钉的是它**真的会发生**：这条断言此前只写在我给用户的回答里，
// 而那句话是读配置读出来的，没有量过 —— 同一个位置上我此前断言过
// 「Caddy 会追加 X-Forwarded-For」，实测是**替换**。
//
// 走 v4 回环的测试碰不到它：EdgeTCP 拨 127.0.0.1，永远落在 0.0.0.0/0 里。
func TestIPv4OnlyWhitelistBlocksIPv6Clients(t *testing.T) {
	r := newRig(t, caddytest.EdgeTCP6())
	setupSite(t, r, "v6.example.com")

	// 没规则时先通一次 —— 否则下面的 404 可能是 IPv6 监听本身没起来。
	if code, _ := r.curlVia("v6.example.com"); code != 200 {
		t.Fatalf("装置坏了：还没加规则，IPv6 上就通不过（%d）", code)
	}

	r.mustDo("PUT", "/rules/wl6", map[string]any{
		"name": "以为是放行所有人", "type": "ip_whitelist", "enabled": true,
		"apply_to": []string{"v6.example.com"},
		"spec":     map[string]any{"ips": []string{"0.0.0.0/0"}},
	})
	r.deployNow("rule:wl6")

	if code, _ := r.curlVia("v6.example.com"); code != 404 {
		t.Errorf("白名单只写了 IPv4 段时，IPv6 访客应当被拦，实际 %d —— "+
			"如果这里是 200，说明 remote_ip 跨地址族的行为跟我说给用户的相反", code)
	}
}

// TestWhitelistWithBothFamiliesLetsIPv6Through 是上一条的**修法**。
//
// 少了它，上一条的绿可以由「白名单无条件拦 v6」达成 —— 那样 `::/0`
// 就成了一句填得进去、看着对、而不起作用的配置。**而界面此刻正建议人加它。**
func TestWhitelistWithBothFamiliesLetsIPv6Through(t *testing.T) {
	r := newRig(t, caddytest.EdgeTCP6())
	setupSite(t, r, "v6ok.example.com")

	r.mustDo("PUT", "/rules/wl6ok", map[string]any{
		"name": "真的放行所有人", "type": "ip_whitelist", "enabled": true,
		"apply_to": []string{"v6ok.example.com"},
		"spec":     map[string]any{"ips": []string{"0.0.0.0/0", "::/0"}},
	})
	r.deployNow("rule:wl6ok")

	if code, _ := r.curlVia("v6ok.example.com"); code != 200 {
		t.Errorf("白名单同时写了两个族时，IPv6 访客应当通过，实际 %d —— "+
			"界面正建议人加 ::/0，它不起作用的话那句建议是空的", code)
	}
}
