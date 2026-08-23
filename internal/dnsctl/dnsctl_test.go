package dnsctl_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xltxb/edge_caddy/internal/dnsctl"
	"github.com/xltxb/edge_caddy/internal/dnssched"
)

// 这些测试打的是**模拟服务端**，不碰真实 API。
//
// issue #21 的验收要求如此，而且拿真账号去试探一个会改写整个 zone 的接口
// 不是个好主意。代价要说清楚：它们验的是「我们按理解发出了正确的请求」，
// 验不了「服务商真的接受这些请求」。线路名与字段名在接入真账号时
// 应当先用只读接口核对一遍。

type call struct {
	Method string
	Path   string
	Form   map[string]string
	Body   map[string]any
}

type fakeAPI struct {
	mu    sync.Mutex
	calls []call
	// respond 按 "方法 路径" 索引。**不能只按路径**：Cloudflare 的 list 与 create
	// 是同一个路径的不同方法，一个要数组一个要对象，只按路径会喂错形状。
	respond map[string]string
}

func (f *fakeAPI) server(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c := call{Method: r.Method, Path: r.URL.Path}

		if strings.Contains(r.Header.Get("Content-Type"), "form-urlencoded") {
			c.Form = map[string]string{}
			if vals, err := parseForm(string(body)); err == nil {
				c.Form = vals
			}
		} else if len(body) > 0 {
			_ = json.Unmarshal(body, &c.Body)
		}

		f.mu.Lock()
		f.calls = append(f.calls, c)
		resp, ok := f.respond[r.Method+" "+r.URL.Path]
		if !ok {
			resp, ok = f.respond[r.URL.Path]
		}
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if !ok {
			resp = `{"success":true,"result":{"id":"generated"}}`
		}
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func parseForm(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range strings.Split(s, "&") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		out[k] = urlDecode(v)
	}
	return out, nil
}

func urlDecode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '+':
			b.WriteByte(' ')
		case s[i] == '%' && i+2 < len(s):
			var n int
			_, _ = fmtSscanHex(s[i+1:i+3], &n)
			b.WriteByte(byte(n))
			i += 2
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func fmtSscanHex(s string, n *int) (int, error) {
	v := 0
	for _, c := range s {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= int(c - '0')
		case c >= 'a' && c <= 'f':
			v |= int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= int(c-'A') + 10
		}
	}
	*n = v
	return 1, nil
}

func (f *fakeAPI) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func node(id, ip string) dnssched.NodeState {
	return dnssched.NodeState{ID: id, IP: ip, DNSEnabled: true, Status: "ok"}
}

// --- DNSPod ---

func TestDNSPodCreatesWeightedRecordsPerLine(t *testing.T) {
	api := &fakeAPI{respond: map[string]string{
		"/Record.List":   `{"status":{"code":"10","message":"No records"}}`,
		"/Record.Create": `{"status":{"code":"1","message":"Action completed successful"}}`,
	}}
	d := dnsctl.NewDNSPod("12345,tok", "example.com", "cdn")
	d.Base = api.server(t)

	plan := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"a": 60, "b": 40}},
		[]dnssched.NodeState{node("a", "1.1.1.1"), node("b", "2.2.2.2")})

	if err := d.Sync(context.Background(), plan); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	var creates int
	for _, c := range api.seen() {
		if c.Path != "/Record.Create" {
			continue
		}
		creates++
		if c.Form["record_line"] != "电信" {
			t.Errorf("线路 = %q，想要 电信", c.Form["record_line"])
		}
		if c.Form["record_type"] != "A" || c.Form["sub_domain"] != "cdn" {
			t.Errorf("记录形状不对: %+v", c.Form)
		}
		if c.Form["weight"] == "" {
			t.Error("权重必须带上——DNSPod 原生支持它，不带就等于放弃了这个功能")
		}
	}
	if creates != 2 {
		t.Fatalf("建了 %d 条记录，想要 2", creates)
	}
}

// 幂等：同一份 Plan 再同步一次不该重复建记录。
// 自愈会在节点抖动时反复调它。
func TestDNSPodSyncIsIdempotent(t *testing.T) {
	api := &fakeAPI{respond: map[string]string{
		"/Record.List": `{"status":{"code":"1"},"records":[
			{"id":"1","name":"cdn","line":"电信","type":"A","value":"1.1.1.1","weight":60}]}`,
	}}
	d := dnsctl.NewDNSPod("12345,tok", "example.com", "cdn")
	d.Base = api.server(t)

	plan := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"a": 60}},
		[]dnssched.NodeState{node("a", "1.1.1.1")})

	if err := d.Sync(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	for _, c := range api.seen() {
		if c.Path == "/Record.Create" || c.Path == "/Record.Modify" {
			t.Fatalf("已经一致的记录不该被再动一次: %s", c.Path)
		}
	}
}

// 摘除的节点会被删掉，**而且是在新记录就位之后才删**——
// 先删后建会有一个「这个域名没有任何 A 记录」的窗口。
func TestDNSPodRemovesNodeThatLeftRotation(t *testing.T) {
	api := &fakeAPI{respond: map[string]string{
		"/Record.List": `{"status":{"code":"1"},"records":[
			{"id":"1","name":"cdn","line":"电信","type":"A","value":"1.1.1.1","weight":50},
			{"id":"2","name":"cdn","line":"电信","type":"A","value":"2.2.2.2","weight":50}]}`,
		"/Record.Modify": `{"status":{"code":"1"}}`,
		"/Record.Remove": `{"status":{"code":"1"}}`,
	}}
	d := dnsctl.NewDNSPod("12345,tok", "example.com", "cdn")
	d.Base = api.server(t)

	gone := node("b", "2.2.2.2")
	gone.DNSEnabled = false
	plan := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"a": 50, "b": 50}},
		[]dnssched.NodeState{node("a", "1.1.1.1"), gone})

	if err := d.Sync(context.Background(), plan); err != nil {
		t.Fatal(err)
	}

	seen := api.seen()
	var removeAt, modifyAt = -1, -1
	for i, c := range seen {
		switch c.Path {
		case "/Record.Remove":
			if c.Form["record_id"] != "2" {
				t.Errorf("删的是 %q，想要 2", c.Form["record_id"])
			}
			removeAt = i
		case "/Record.Modify":
			modifyAt = i
		}
	}
	if removeAt < 0 {
		t.Fatal("退出轮换的节点应当被删除")
	}
	if modifyAt >= 0 && removeAt < modifyAt {
		t.Error("删除发生在改写之前——中间会有一个没有任何 A 记录的窗口")
	}
}

// 不认识的线路上的记录不动：别人手工加的东西不该被这套系统清掉。
func TestDNSPodLeavesUnmanagedLinesAlone(t *testing.T) {
	api := &fakeAPI{respond: map[string]string{
		"/Record.List": `{"status":{"code":"1"},"records":[
			{"id":"9","name":"cdn","line":"教育网","type":"A","value":"9.9.9.9","weight":1}]}`,
		"/Record.Remove": `{"status":{"code":"1"}}`,
	}}
	d := dnsctl.NewDNSPod("12345,tok", "example.com", "cdn")
	d.Base = api.server(t)

	plan := dnssched.Build("cdn.example.com", dnssched.Weights{}, nil)
	if err := d.Sync(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	for _, c := range api.seen() {
		if c.Path == "/Record.Remove" {
			t.Fatalf("不该动我们不管的线路上的记录: %+v", c.Form)
		}
	}
}

// 服务商的报错原文要往上传：那是排查凭证/套餐/线路不可用的唯一线索。
func TestDNSPodPropagatesProviderMessage(t *testing.T) {
	api := &fakeAPI{respond: map[string]string{
		"/Record.List":   `{"status":{"code":"10"}}`,
		"/Record.Create": `{"status":{"code":"-15","message":"域名已锁定"}}`,
	}}
	d := dnsctl.NewDNSPod("12345,tok", "example.com", "cdn")
	d.Base = api.server(t)

	plan := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"a": 1}}, []dnssched.NodeState{node("a", "1.1.1.1")})

	err := d.Sync(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "域名已锁定") {
		t.Fatalf("应当带上服务商的原文，实际 %v", err)
	}
}

// --- Cloudflare ---

// **电信/联通/移动权重不同时明确报错。**
//
// Cloudflare 的地理维度是国家，这三条线在那边表达不了。取个平均值会给出
// 一个用户没要过的配置，而且没人会发现——拒绝才是对的。
func TestCloudflareRefusesDivergentChinaLines(t *testing.T) {
	api := &fakeAPI{}
	cf := dnsctl.NewCloudflare("acct", "zone", "cdn.example.com")
	cf.Token = "tok"
	cf.Base = api.server(t)

	plan := dnssched.Build("cdn.example.com", dnssched.Weights{
		"ct": {"a": 60, "b": 40},
		"cu": {"a": 60, "b": 40},
		"cm": {"a": 90, "b": 10}, // 移动不一样
	}, []dnssched.NodeState{node("a", "1.1.1.1"), node("b", "2.2.2.2")})

	err := cf.Sync(context.Background(), plan)
	if err == nil {
		t.Fatal("三条线权重不同时应当拒绝，而不是悄悄取个平均值")
	}
	var capErr *dnsctl.ErrCapability
	if !errors.As(err, &capErr) {
		t.Fatalf("应当报成能力不足（好让界面说清「这家服务商做不到」），实际 %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "移动") {
		t.Errorf("报错应当指出是哪条线不一致: %v", err)
	}
	if len(api.seen()) > 0 {
		t.Error("被拒绝的配置不该已经打过服务商的接口")
	}
}

func TestCloudflareSyncsPoolsAndLoadBalancer(t *testing.T) {
	api := &fakeAPI{respond: map[string]string{
		"GET /accounts/acct/load_balancers/pools":  `{"success":true,"result":[]}`,
		"POST /accounts/acct/load_balancers/pools": `{"success":true,"result":{"id":"pool-1"}}`,
		"GET /zones/zone/load_balancers":           `{"success":true,"result":[]}`,
		"POST /zones/zone/load_balancers":          `{"success":true,"result":{"id":"lb-1"}}`,
	}}
	cf := dnsctl.NewCloudflare("acct", "zone", "cdn.example.com")
	cf.Token = "tok"
	cf.Base = api.server(t)

	plan := dnssched.Build("cdn.example.com", dnssched.Weights{
		"ct": {"a": 60, "b": 40},
		"cu": {"a": 60, "b": 40},
		"cm": {"a": 60, "b": 40},
		"ov": {"b": 100},
	}, []dnssched.NodeState{node("a", "1.1.1.1"), node("b", "2.2.2.2")})

	if err := cf.Sync(context.Background(), plan); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	var sawPoolCreate, sawLB bool
	for _, c := range api.seen() {
		if c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/load_balancers/pools") {
			sawPoolCreate = true
			origins, _ := c.Body["origins"].([]any)
			var sum float64
			for _, o := range origins {
				m := o.(map[string]any)
				sum += m["weight"].(float64)
			}
			// Cloudflare 的 origin weight 是 0–1 的小数，不是我们那个整数。
			if sum < 0.99 || sum > 1.01 {
				t.Errorf("pool 内权重之和 = %v，想要 1（Cloudflare 用的是比例）", sum)
			}
		}
		if c.Method == http.MethodPost && c.Path == "/zones/zone/load_balancers" {
			sawLB = true
			cp, _ := c.Body["country_pools"].(map[string]any)
			if _, ok := cp["CN"]; !ok {
				t.Errorf("应当把中国映射到 cn pool: %+v", c.Body)
			}
			if c.Body["steering_policy"] != "geo" {
				t.Errorf("steering_policy = %v", c.Body["steering_policy"])
			}
		}
	}
	if !sawPoolCreate || !sawLB {
		t.Fatalf("应当同时建 pool 与 load balancer，实际调用 %+v", api.seen())
	}
}

// 两种凭证模式（PRD §5 明确要区分）。
func TestCloudflareCredentialModes(t *testing.T) {
	// 必须用一份**有节点在轮换里**的 plan：空 plan 会在打接口之前就短路返回，
	// 于是这几条断言什么也验不到——那正是「断言的对象覆盖不到要验的性质」。
	plan := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"a": 1}, "cu": {"a": 1}, "cm": {"a": 1}},
		[]dnssched.NodeState{node("a", "1.1.1.1")})

	t.Run("API Token 走 Bearer", func(t *testing.T) {
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			_, _ = io.WriteString(w, `{"success":true,"result":[]}`)
		}))
		t.Cleanup(srv.Close)

		cf := dnsctl.NewCloudflare("acct", "zone", "cdn.example.com")
		cf.Token, cf.Base = "tok", srv.URL
		_ = cf.Sync(context.Background(), plan)
		if gotAuth != "Bearer tok" {
			t.Fatalf("Authorization = %q", gotAuth)
		}
	})

	t.Run("Global Key 走 X-Auth 头", func(t *testing.T) {
		var email, key string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			email, key = r.Header.Get("X-Auth-Email"), r.Header.Get("X-Auth-Key")
			_, _ = io.WriteString(w, `{"success":true,"result":[]}`)
		}))
		t.Cleanup(srv.Close)

		cf := dnsctl.NewCloudflare("acct", "zone", "cdn.example.com")
		cf.Email, cf.GlobalKey, cf.Base = "ops@example.com", "globalkey", srv.URL
		_ = cf.Sync(context.Background(), plan)
		if email != "ops@example.com" || key != "globalkey" {
			t.Fatalf("X-Auth-Email=%q X-Auth-Key=%q", email, key)
		}
	})

	t.Run("没有凭证时明确报错", func(t *testing.T) {
		cf := dnsctl.NewCloudflare("acct", "zone", "cdn.example.com")
		cf.Base = "http://127.0.0.1:1"
		err := cf.Sync(context.Background(), plan)
		if err == nil || !strings.Contains(err.Error(), "凭证未配置") {
			t.Fatalf("应当说清是凭证没配，实际 %v", err)
		}
	})
}

// Caps 要如实说出做不到什么 —— 界面据此把无效的输入框置灰。
func TestCapabilitiesAreHonest(t *testing.T) {
	cf := dnsctl.NewCloudflare("a", "z", "h").Caps()
	for _, l := range cf.Lines {
		if l.Code == "ct" || l.Code == "cu" || l.Code == "cm" {
			t.Fatalf("Cloudflare 不该声称能区分 %s —— 它的地理维度是国家", l.Code)
		}
	}
	// 覆盖关系必须由服务商给出：那是它的知识。放在前端意味着同一份知识
	// 存在两处，加第三家服务商时会静默渲染错。
	var cn *struct{ n int }
	for _, l := range cf.Lines {
		if l.Code != "cn" {
			continue
		}
		cn = &struct{ n int }{len(l.Covers)}
		want := map[string]bool{"ct": true, "cu": true, "cm": true}
		for _, c := range l.Covers {
			if !want[c] {
				t.Errorf("中国这一组不该盖住 %s", c)
			}
			delete(want, c)
		}
		if len(want) > 0 {
			t.Errorf("中国这一组漏了 %v", want)
		}
	}
	if cn == nil {
		t.Fatal("Cloudflare 应当给出一个「中国」分组")
	}

	// 五条线路必须被完整覆盖，一条都不能漏 —— 漏掉的那条在界面上会凭空消失。
	covered := map[string]bool{}
	for _, l := range cf.Lines {
		for _, c := range l.Covers {
			covered[c] = true
		}
	}
	for _, code := range []string{"ct", "cu", "cm", "tw", "ov"} {
		if !covered[code] {
			t.Errorf("线路 %s 没有被任何分组覆盖", code)
		}
	}
	if !strings.Contains(cf.Notes, "无法区分") {
		t.Errorf("说明里应当讲清这个限制: %q", cf.Notes)
	}

	dp := dnsctl.NewDNSPod("t", "d", "s").Caps()
	if len(dp.Lines) != 5 || !dp.Weights {
		t.Errorf("DNSPod 原生支持五条线与权重: %+v", dp)
	}
	for _, l := range dp.Lines {
		if len(l.Covers) != 1 || l.Covers[0] != l.Code {
			t.Errorf("DNSPod 能逐条区分，每组应当只盖住自己: %+v", l)
		}
	}
}

// TestCloudflareRefusesEmptyPathSegment 钉的是**空路径段就地拦下，不发出去**。
//
// account_id 为空时 URL 会拼成 `/accounts//load_balancers/pools`，
// 而 Cloudflare 回的是 7003「Could not route to ...，perhaps your object
// identifier is invalid?」——它把人送去查 token 和权限，
// 而真正的原因是一个字段没填。**远端的错误消息不认识我们的字段名。**
//
// 校验那一侧（store.MissingFields）现在会拦住这种配置，所以正常情况下
// 走不到这里。这条守的是下一个被拼进 URL 的字段：那个判据在另一个包里，
// 而这道防线就在拼接的旁边。
func TestCloudflareRefusesEmptyPathSegment(t *testing.T) {
	api := &fakeAPI{respond: map[string]string{}}
	cf := dnsctl.NewCloudflare("", "zone", "cdn.example.com") // account_id 空
	cf.Token = "tok"
	cf.Base = api.server(t)

	plan := dnssched.Build("cdn.example.com", dnssched.Weights{
		"ct": {"a": 100}, "cu": {"a": 100}, "cm": {"a": 100}, "ov": {"a": 100},
	}, []dnssched.NodeState{node("a", "1.1.1.1")})

	err := cf.Sync(context.Background(), plan)
	if err == nil {
		t.Fatal("account_id 为空却没报错 —— 这个请求会被发出去，" +
			"换回一句和「少填了一项」毫无关系的「路由不到」")
	}
	if !strings.Contains(err.Error(), "account_id") {
		t.Errorf("报错没点名是哪个字段：%v", err)
	}
	if n := len(api.seen()); n != 0 {
		t.Errorf("发出了 %d 个请求 —— 应该在拼接那一步就停住，一个都不发", n)
	}
}

// TestCloudflareAuthErrorSaysWhichPermission 钉的是**远端的错误消息不认识我们的场景**。
//
// 灰度上的原话：`10000 Authentication error`。它对，而它说不出该去改哪里——
// 人会去重新生成一个 Token，而新 Token 多半还是只有 DNS 权限，于是再撞一次。
//
// 加权调度要两处权限，在 Cloudflare 的 Token 编辑页是两个不同的区块，
// 而「只给 DNS 权限」是最常见的那一种。原始措辞仍然原样带上——
// 它是一手证据，这只是在它后面补一句说得出动作的话。
func TestCloudflareAuthErrorSaysWhichPermission(t *testing.T) {
	api := &fakeAPI{respond: map[string]string{
		"GET /accounts/acct/load_balancers/pools": `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`,
	}}
	cf := dnsctl.NewCloudflare("acct", "zone", "cdn.example.com")
	cf.Token = "tok"
	cf.Base = api.server(t)

	plan := dnssched.Build("cdn.example.com", dnssched.Weights{
		"ct": {"a": 100}, "cu": {"a": 100}, "cm": {"a": 100}, "ov": {"a": 100},
	}, []dnssched.NodeState{node("a", "1.1.1.1")})

	err := cf.Sync(context.Background(), plan)
	if err == nil {
		t.Fatal("10000 却当成功了")
	}
	got := err.Error()
	if !strings.Contains(got, "Authentication error") {
		t.Errorf("丢掉了服务商的原话，那是唯一的一手证据：%v", got)
	}
	for _, want := range []string{"Load Balancing", "Edit", "付费"} {
		if !strings.Contains(got, want) {
			t.Errorf("没说出该去改什么（缺 %q）：%v", want, got)
		}
	}
}

// --- Cloudflare 纯 DNS ---

func plainPlan(t *testing.T, nodes ...dnssched.NodeState) dnssched.Plan {
	t.Helper()
	w := dnssched.Weights{}
	for _, l := range []string{"ct", "cu", "cm", "tw", "ov"} {
		m := map[string]int{}
		for _, n := range nodes {
			m[n.ID] = 100
		}
		w[l] = m
	}
	return dnssched.Build("cdn.example.com", w, nodes)
}

// TestCloudflareDNSAddsBeforeDeleting 钉的是**先加后删**。
//
// 反过来的话，中间会有一个「旧记录都删了、新记录还没建上」的窗口，
// 那期间这个域名**解析不出来**——而那不是降级，是彻底不可达。
//
// 这条不看返回值，只看调用顺序：正确性完全在时序里，
// 而时序是那种「跑一百次都不出错、出错时是灾难」的东西。
func TestCloudflareDNSAddsBeforeDeleting(t *testing.T) {
	api := &fakeAPI{respond: map[string]string{
		// 现状：一条旧记录（1.1.1.1），要换成 2.2.2.2
		"GET /zones/z/dns_records": `{"success":true,"result":[
			{"id":"rec-old","type":"A","name":"cdn.example.com","content":"1.1.1.1"}]}`,
		"POST /zones/z/dns_records":           `{"success":true,"result":{"id":"rec-new"}}`,
		"DELETE /zones/z/dns_records/rec-old": `{"success":true,"result":{}}`,
	}}
	cf := dnsctl.NewCloudflareDNS("z", "cdn.example.com")
	cf.Token = "tok"
	cf.Base = api.server(t)

	if err := cf.Sync(context.Background(), plainPlan(t, node("b", "2.2.2.2"))); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	var postAt, deleteAt = -1, -1
	for i, c := range api.seen() {
		switch {
		case c.Method == "POST" && strings.Contains(c.Path, "/dns_records"):
			postAt = i
		case c.Method == "DELETE" && strings.Contains(c.Path, "/dns_records/"):
			deleteAt = i
		}
	}
	if postAt < 0 || deleteAt < 0 {
		t.Fatalf("装置坏了：没看到 POST(%d) / DELETE(%d)，调用是 %+v",
			postAt, deleteAt, api.seen())
	}
	if postAt > deleteAt {
		t.Error("先删后加 —— 中间那个窗口里这个域名解析不出来")
	}
}

// TestCloudflareDNSNeverProxies 钉的是 proxied 必须是 false。
//
// **打开橙云的话，到达用户的是 Cloudflare 的边缘，不是我们的节点**，
// 这套系统就成了一个没人经过的摆设。而它最难查的地方在于**它看起来是好的**：
// 域名能打开、证书也正常，只有回源日志是空的。
func TestCloudflareDNSNeverProxies(t *testing.T) {
	api := &fakeAPI{respond: map[string]string{
		"GET /zones/z/dns_records":  `{"success":true,"result":[]}`,
		"POST /zones/z/dns_records": `{"success":true,"result":{"id":"r1"}}`,
	}}
	cf := dnsctl.NewCloudflareDNS("z", "cdn.example.com")
	cf.Token = "tok"
	cf.Base = api.server(t)

	if err := cf.Sync(context.Background(), plainPlan(t, node("a", "1.1.1.1"))); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	var sawPost bool
	for _, c := range api.seen() {
		if c.Method != "POST" {
			continue
		}
		sawPost = true
		if p, ok := c.Body["proxied"]; !ok || p != false {
			t.Errorf("proxied=%v（要 false）—— 开了橙云，流量到不了我们的节点", p)
		}
		if c.Body["type"] != "A" {
			t.Errorf("IPv4 该建 A 记录，实际 %v", c.Body["type"])
		}
	}
	if !sawPost {
		t.Fatal("装置坏了：一个 POST 都没发出去")
	}
}

// TestCloudflareDNSRefusesWhatItCannotExpress 钉的是**塌缩要报错，不能默默取平均**。
//
// 普通 DNS 记录既没有权重也没有线路。按等权处理的话，界面上权重条画着 60/40
// 而实际是轮询，**而那种不一致没有任何地方会说出来**。
func TestCloudflareDNSRefusesWhatItCannotExpress(t *testing.T) {
	cf := dnsctl.NewCloudflareDNS("z", "cdn.example.com")
	cf.Token = "tok"
	cf.Base = "http://127.0.0.1:1" // 不该被用到

	nodes := []dnssched.NodeState{node("a", "1.1.1.1"), node("b", "2.2.2.2")}

	// 一、权重不同
	w := dnssched.Weights{}
	for _, l := range []string{"ct", "cu", "cm", "tw", "ov"} {
		w[l] = map[string]int{"a": 60, "b": 40}
	}
	err := cf.Sync(context.Background(), dnssched.Build("cdn.example.com", w, nodes))
	if err == nil {
		t.Fatal("权重不同却收下了 —— 界面画 60/40 而实际轮询，没人会发现")
	}
	if !strings.Contains(err.Error(), "权重") {
		t.Errorf("报错要说清是权重的问题：%v", err)
	}

	// 二、线路配得不一样
	w2 := dnssched.Weights{
		"ct": {"a": 100}, "cu": {"a": 100}, "cm": {"a": 100},
		"tw": {"b": 100}, "ov": {"a": 100},
	}
	err = cf.Sync(context.Background(), dnssched.Build("cdn.example.com", w2, nodes))
	if err == nil {
		t.Fatal("五条线配得不一样却收下了")
	}
	if !strings.Contains(err.Error(), "线路") {
		t.Errorf("报错要说清是线路的问题：%v", err)
	}
}

// TestCloudflareDNSKeepsRecordsWhenNothingIsInRotation：
// 一个节点都不在轮换里时**不清空记录**。
//
// 把最后一条记录撤掉等于主动让域名解析不出来，而「全体离线」多半是短暂的。
// 宁可让流量继续打到已知的机器上，也不要主动制造一次 NXDOMAIN。
func TestCloudflareDNSKeepsRecordsWhenNothingIsInRotation(t *testing.T) {
	api := &fakeAPI{respond: map[string]string{}}
	cf := dnsctl.NewCloudflareDNS("z", "cdn.example.com")
	cf.Token = "tok"
	cf.Base = api.server(t)

	dead := dnssched.NodeState{ID: "a", IP: "1.1.1.1", DNSEnabled: true, Status: "down"}
	err := cf.Sync(context.Background(), plainPlan(t, dead))
	if err == nil {
		t.Fatal("一个节点都没有时该明确报错，而不是默默把记录清空")
	}
	if n := len(api.seen()); n != 0 {
		t.Errorf("发出了 %d 个请求 —— 这种情况一个都不该发", n)
	}
}
