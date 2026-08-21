package render_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
)

func issueFields(is []render.Issue) []string {
	out := make([]string, 0, len(is))
	for _, i := range is {
		out = append(out, i.Field)
	}
	return out
}

func hasField(is []render.Issue, field string) bool {
	for _, i := range is {
		if i.Field == field {
			return true
		}
	}
	return false
}

func ok(d, u string) model.Route {
	return model.Route{Domain: d, Upstream: u, BlockMode: model.BlockAbort}
}

// body_max 在 API 与库里是人类可读字符串，Caddy 的 max_size 要 int64 字节数。
// 这个转换只有渲染器这一处——ADR-0007 举的正是这个例子：原样下发会被整份拒绝。
func TestBodyMaxIsConvertedToBytes(t *testing.T) {
	r := ok("api.example.com", "127.0.0.1:8080")
	r.BodyMax = "5MB"
	b, issues := render.Render([]model.Route{r}, nil, nil, render.Policies{}, render.Options{})
	if len(issues) > 0 {
		t.Fatalf("不该有校验问题: %v", issues)
	}

	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	if strings.Contains(raw, `"max_size": "5MB"`) {
		t.Fatal("max_size 仍是字符串——这份配置会被 Caddy 整份拒绝")
	}
	if !strings.Contains(raw, `"max_size": 5242880`) {
		t.Fatalf("max_size 应当是 5242880 字节（5 × 1024²），实际产出:\n%s", raw)
	}
}

func TestBodyMaxUnits(t *testing.T) {
	cases := map[string]string{
		"512KB": "524288",
		"1GB":   "1073741824",
		"1024":  "1024", // 无单位即字节
	}
	for in, want := range cases {
		r := ok("a.example.com", "127.0.0.1:1")
		r.BodyMax = in
		b, issues := render.Render([]model.Route{r}, nil, nil, render.Policies{}, render.Options{})
		if len(issues) > 0 {
			t.Errorf("%s: %v", in, issues)
			continue
		}
		if !strings.Contains(string(b), `"max_size": `+want) {
			t.Errorf("%s 应当转成 %s 字节", in, want)
		}
	}
}

// 校验必须一次报出**全部**问题。只报第一个会让人改一处推一次，来回好几轮，
// 而「改配置怕推错」正是这套东西要解决的痛点。
func TestValidateReportsAllIssuesNotJustTheFirst(t *testing.T) {
	issues := render.Validate([]model.Route{
		{Domain: "not a domain", Upstream: "no-port", BlockMode: "毁灭", BodyMax: "五兆",
			Whitelist: []string{"10.8.0.0/33"}},
	}, nil)
	for _, want := range []string{"domain", "upstream", "block_mode", "body_max", "whitelist[0]"} {
		if !hasField(issues, want) {
			t.Errorf("缺少字段 %s 的问题；实际报了 %v", want, issueFields(issues))
		}
	}
}

// 字段路径要能对上前端表单，数组下标用 [n]（api-contract §0.3）。
func TestIssueFieldPathUsesArrayIndex(t *testing.T) {
	issues := render.Validate([]model.Route{{
		Domain: "a.example.com", Upstream: "127.0.0.1:1", BlockMode: model.BlockAbort,
		Whitelist: []string{"10.0.0.0/8", "不是IP", "192.168.1.1"},
	}}, nil)
	if !hasField(issues, "whitelist[1]") {
		t.Fatalf("应当把问题定位到 whitelist[1]，实际 %v", issueFields(issues))
	}
	if hasField(issues, "whitelist[0]") || hasField(issues, "whitelist[2]") {
		t.Errorf("合法的条目被误报了: %v", issueFields(issues))
	}
}

func TestUpstreamMustBeHostPort(t *testing.T) {
	for _, bad := range []string{"", "example.com", "example.com:", "example.com:0", "example.com:99999", "example.com:abc"} {
		issues := render.Validate([]model.Route{{Domain: "a.example.com", Upstream: bad, BlockMode: model.BlockAbort}}, nil)
		if !hasField(issues, "upstream") {
			t.Errorf("回源地址 %q 应当被拒绝", bad)
		}
	}
	for _, good := range []string{"127.0.0.1:8080", "origin.internal:443", "10.8.0.12:80"} {
		issues := render.Validate([]model.Route{{Domain: "a.example.com", Upstream: good, BlockMode: model.BlockAbort}}, nil)
		if hasField(issues, "upstream") {
			t.Errorf("回源地址 %q 是合法的，却被拒绝: %v", good, issues)
		}
	}
}

func TestDuplicateDomainIsRejected(t *testing.T) {
	issues := render.Validate([]model.Route{
		ok("dup.example.com", "127.0.0.1:1"),
		ok("dup.example.com", "127.0.0.1:2"),
	}, nil)
	if !hasField(issues, "domain") {
		t.Fatal("重复域名应当被拒绝")
	}
}

// **回源 mTLS 是「边缘向源站出示客户端证书」**，不是「要求访问者出示证书」
// （ADR-0008）。两者方向相反。
//
// 渲染成 reverse_proxy.transport.tls，**绝不碰 tls_connection_policies**：
// 那会让整台 server 转 TLS，同节点上所有没有服务端证书的域名会立即失联。
func TestMTLSRendersAsUpstreamClientCertNotConnectionPolicy(t *testing.T) {
	r := ok("m.example.com", "127.0.0.1:1")
	r.MTLS = true

	b, issues := render.Render([]model.Route{r}, nil, nil, render.Policies{}, render.Options{
		UpstreamClientCert: "/var/lib/edge-agent/edge-mtls.crt",
		UpstreamClientKey:  "/var/lib/edge-agent/edge-mtls.key",
	})
	if len(issues) > 0 {
		t.Fatalf("不该有校验问题: %v", issues)
	}
	out := string(b)

	if !strings.Contains(out, `"client_certificate_file": "/var/lib/edge-agent/edge-mtls.crt"`) {
		t.Fatalf("应当渲染成回源时出示客户端证书:\n%s", out)
	}
	// 没有证书时不该出现 tls_connection_policies —— 那是「要求访问者出示证书」
	// 那条读法才需要的，而它会让整台 server 转 TLS。
	if strings.Contains(out, "tls_connection_policies") {
		t.Fatalf("回源 mTLS 不该碰 tls_connection_policies:\n%s", out)
	}
	// 也不该用设计稿的 client_certificate_automate：那要求每台节点持有 CA 私钥，
	// 6 台节点会各自成为独立的 CA，源站得同时信任 6 个根（ADR-0008）。
	if strings.Contains(out, "client_certificate_automate") {
		t.Fatal("不该用 client_certificate_automate —— 它要求节点持有 CA 私钥")
	}
}

// 一张证书都没有时**完全不渲染 apps/tls，也不渲染 :443**（ADR-0010）。
//
// 渲染空的 tls app 会把节点上外部证书平台写入的内容抹掉——那是上一版真出过
// 的事故。而一个没有证书的 :443 监听会让每一次握手都失败，比不监听更糟。
func TestNoCertsMeansNoTLSAppAndNoHTTPSServer(t *testing.T) {
	b, _ := render.Render([]model.Route{ok("t.example.com", "127.0.0.1:1")}, nil, nil, render.Policies{}, render.Options{})
	var cfg struct {
		Apps struct {
			TLS  json.RawMessage `json:"tls"`
			HTTP struct {
				Servers map[string]json.RawMessage `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Apps.TLS != nil {
		t.Fatal("没有证书时不该渲染 apps/tls")
	}
	if _, ok := cfg.Apps.HTTP.Servers["edge_tls"]; ok {
		t.Fatal("没有证书时不该渲染 :443 那台 server")
	}
}

// 有证书时用 load_pem **内联**，并单独渲染一台 :443。
func TestCertsAreInlinedAndTLSServerAppears(t *testing.T) {
	certs := []render.Cert{{
		Domain:  "t.example.com",
		CertPEM: []byte("-----BEGIN CERTIFICATE-----\nAAA\n-----END CERTIFICATE-----\n"),
		KeyPEM:  []byte("-----BEGIN EC PRIVATE KEY-----\nBBB\n-----END EC PRIVATE KEY-----\n"),
	}}
	b, _ := render.Render([]model.Route{ok("t.example.com", "127.0.0.1:1")}, nil, certs, render.Policies{},
		render.Options{HTTPSListen: ":8443"})
	out := string(b)

	if !strings.Contains(out, "load_pem") {
		t.Fatalf("证书应当用 load_pem 内联:\n%s", out)
	}
	// 不用 load_files：落盘要求主控渲染的路径与节点上的实际路径一致，
	// 而那是两个进程各自持有的知识，迟早会不一致（ADR-0010）。
	if strings.Contains(out, "load_files") {
		t.Fatal("不该用 load_files 落盘")
	}
	if !strings.Contains(out, "-----BEGIN EC PRIVATE KEY-----") {
		t.Fatal("私钥要内联进去 —— 那正是 load_pem 的含义")
	}

	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen   []string `json:"listen"`
					Policies []any    `json:"tls_connection_policies"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	tlsSrv, ok := cfg.Apps.HTTP.Servers["edge_tls"]
	if !ok {
		t.Fatal("有证书时应当渲染 :443 那台 server")
	}
	if len(tlsSrv.Policies) != 1 {
		t.Fatalf("那台 server 需要一条空的连接策略才会转 TLS，实际 %+v", tlsSrv.Policies)
	}
	// **:80 那台绝不能带连接策略** —— 加上会让所有没有服务端证书的域名立即失联。
	if plain := cfg.Apps.HTTP.Servers["edge"]; len(plain.Policies) != 0 {
		t.Fatalf(":80 那台不该有连接策略，实际 %+v", plain.Policies)
	}
}

// 白名单为空 = 不限制，不应产生 deny 路由。
func TestEmptyWhitelistProducesNoDenyRoute(t *testing.T) {
	b, _ := render.Render([]model.Route{ok("open.example.com", "127.0.0.1:1")}, nil, nil, render.Policies{}, render.Options{})
	// 先确认这份渲染确实产出了那条路由。没这一句的话，Render 返回空
	// （或者哪天签名变了）也会让下面那个「不含 not」成立 ——
	// 而「没有 deny 匹配器」和「什么都没渲染出来」是两回事。
	if !strings.Contains(string(b), "open.example.com") {
		t.Fatalf("渲染结果里没有那条路由，下面的断言无从谈起:\n%s", b)
	}
	if strings.Contains(string(b), `"not"`) {
		t.Fatalf("白名单为空却渲染出了 deny 匹配器:\n%s", b)
	}
}

// 裸 IP 补成 /32，让渲染产出稳定——写法差异不该在 diff 里跳行。
func TestBareIPIsNormalizedToCIDR(t *testing.T) {
	r := ok("w.example.com", "127.0.0.1:1")
	r.Whitelist = []string{"203.0.113.7", "10.8.0.0/24"}
	b, _ := render.Render([]model.Route{r}, nil, nil, render.Policies{}, render.Options{})
	if !strings.Contains(string(b), `"203.0.113.7/32"`) {
		t.Errorf("裸 IP 应当补成 /32:\n%s", b)
	}
	if !strings.Contains(string(b), `"10.8.0.0/24"`) {
		t.Error("已经是 CIDR 的条目不该被改动")
	}
}

// 渲染产出必须与输入顺序无关，否则 diff 会因为「谁先谁后」而虚报变更。
func TestRenderIsOrderIndependent(t *testing.T) {
	a := []model.Route{ok("b.example.com", "127.0.0.1:1"), ok("a.example.com", "127.0.0.1:2")}
	b := []model.Route{ok("a.example.com", "127.0.0.1:2"), ok("b.example.com", "127.0.0.1:1")}
	ra, _ := render.Render(a, nil, nil, render.Policies{}, render.Options{})
	rb, _ := render.Render(b, nil, nil, render.Policies{}, render.Options{})
	if string(ra) != string(rb) {
		t.Fatal("同一组路由换个顺序渲染出了不同的字节——diff 会虚报变更")
	}
}

// 不渲染 apps/tls：一张证书都没有时渲染它会把节点上外部证书平台写入的内容抹掉，
// 那是上一版真出过的事故（ADR-0010）。
func TestDoesNotRenderTLSApp(t *testing.T) {
	b, _ := render.Render([]model.Route{ok("t.example.com", "127.0.0.1:1")}, nil, nil, render.Policies{}, render.Options{})
	var cfg struct {
		Apps map[string]json.RawMessage `json:"apps"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Apps["tls"]; ok {
		t.Fatal("主控还没有任何证书时不该渲染 apps/tls")
	}
}

// 校验不过时不产出配置——一份没通过校验的配置绝不该有机会被下发。
func TestNoConfigWhenValidationFails(t *testing.T) {
	b, issues := render.Render([]model.Route{{Domain: "bad", Upstream: "x"}}, nil, nil, render.Policies{}, render.Options{})
	if len(issues) == 0 {
		t.Fatal("这组输入应当校验失败")
	}
	if b != nil {
		t.Fatal("校验失败时不该产出配置")
	}
}

// 校验错误的字段路径必须指向**请求体里真实存在的**字段。
//
// 契约 §0.3 说前端按这个点号路径去索引表单字段。共享密钥在 PUT /rules/:id 的
// **顶层** secret 上（放进 spec 就等于被 GET /rules 回显），所以路径是 secret
// 而不是 spec.secret——后者匹配不到任何字段，这条错误就会掉在地上，
// 只剩一个笼统的「未通过校验」。
//
// **一条指不到地方的错误信息，等于没有这条错误信息。**
func TestValidationFieldPathsPointAtRealRequestFields(t *testing.T) {
	issues := render.Validate(
		[]model.Route{ok("api.example.com", "127.0.0.1:1")},
		[]model.Rule{{
			ID: "svc-1", Type: model.RuleServiceSecret, Enabled: true,
			ApplyTo: []string{"api.example.com"},
			Spec:    model.RuleSpec{Header: "X-Service-Key", TTLSeconds: 300},
			// 没有 Secret
		}})

	if !hasField(issues, "secret") {
		t.Fatalf("缺密钥的错误应当指向顶层 secret，实际 %v", issueFields(issues))
	}
	if hasField(issues, "spec.secret") {
		t.Fatal("不该指向 spec.secret —— 密钥不在 spec 里，那个路径在请求体里不存在")
	}
}

// TLS 策略的 email 没有默认值，而且不该有：替人编一个邮箱比留空更糟，
// 因为 ACME 账户邮箱是真会被 CA 用来发到期通知的。
func TestTLSPolicyHasNoDefaultEmail(t *testing.T) {
	pol, err := render.ParsePolicies(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pol.TLS.Email != "" {
		t.Fatalf("不该替人编一个 ACME 邮箱，实际 %q", pol.TLS.Email)
	}
	// 其余枚举字段则必须有默认 —— 界面显示空会让人无从知道什么在生效。
	if pol.TLS.MinVersion == "" || pol.TLS.KeyType == "" || pol.TLS.CA == "" {
		t.Fatalf("枚举字段应当有默认值: %+v", pol.TLS)
	}
	if pol.Log.RateLimit {
		t.Fatal("限流默认必须是关的 —— 打开会让下发被拒")
	}
}
