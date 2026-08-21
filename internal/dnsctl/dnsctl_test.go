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
		if l == "ct" || l == "cu" || l == "cm" {
			t.Fatalf("Cloudflare 不该声称能区分 %s —— 它的地理维度是国家", l)
		}
	}
	if !strings.Contains(cf.Notes, "无法区分") {
		t.Errorf("说明里应当讲清这个限制: %q", cf.Notes)
	}

	dp := dnsctl.NewDNSPod("t", "d", "s").Caps()
	if len(dp.Lines) != 5 || !dp.Weights {
		t.Errorf("DNSPod 原生支持五条线与权重: %+v", dp)
	}
}
