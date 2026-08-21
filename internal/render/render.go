// Package render 把配置意图变成边缘节点上的 Caddy 配置。
//
// **它是唯一权威渲染器。** 工作台右栏那份 JSON 是「可读表示」，不是将要下发的字节
// （docs/adr/0007-workbench-preview-is-a-representation.md）。
//
// 主控不安装 Caddy，因此下发前的校验是这里的 Go 层检查，不是 `caddy validate`
// （docs/adr/0004-no-master-side-caddy-validate.md）。这拦不住 Caddy schema 层面的错，
// 那类错由本包的集成测试用真 Caddy 兜住。
package render

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/xltxb/edge_caddy/internal/model"
)

// Options 是渲染的环境参数，不属于配置资源本身。
// Cert 是一张要内联进配置的证书。
type Cert struct {
	Domain  string
	CertPEM []byte
	KeyPEM  []byte
}

type Options struct {
	// HTTPListen 是边缘 server 的监听地址，生产是 ":80"。
	// 做成参数只为让测试能用非特权端口。
	HTTPListen string

	// HTTPSListen 是 TLS server 的监听地址，生产是 ":443"。
	// 只在有证书时才渲染那个 server —— 一个没有证书的 :443 监听
	// 会让每一次握手都失败，比不监听更糟。
	HTTPSListen string

	// UpstreamClientCert / UpstreamClientKey 是节点回源时出示的客户端证书
	// 在**节点本机**的路径（ADR-0008 / ADR-0009）。
	UpstreamClientCert string
	UpstreamClientKey  string

	// VerifyAddr 是 Agent 校验端点在节点回环上的地址。
	// JWT 与服务密钥由它验签，Caddy 只做 forward_auth 委托（ADR-0003）。
	VerifyAddr string
}

func (o Options) withDefaults() Options {
	if o.HTTPListen == "" {
		o.HTTPListen = ":80"
	}
	if o.HTTPSListen == "" {
		o.HTTPSListen = ":443"
	}
	if o.VerifyAddr == "" {
		o.VerifyAddr = "127.0.0.1:2020"
	}
	return o
}

// Issue 是一条校验失败，字段与 api-contract §0.3 的 FieldError 一一对应。
type Issue struct {
	ResKey string `json:"res_key"`
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func (i Issue) String() string { return i.ResKey + "." + i.Field + ": " + i.Reason }

// Render 校验并渲染。校验不过时返回全部问题且不产出配置——
// 只报第一个错会让人改一处推一次，来回好几轮。
func Render(routes []model.Route, rules []model.Rule, certs []Cert, pol Policies, opt Options) ([]byte, []Issue) {
	opt = opt.withDefaults()

	issues := Validate(routes, rules)
	issues = append(issues, validatePolicies(pol)...)
	if len(issues) > 0 {
		return nil, issues
	}

	sorted := append([]model.Route(nil), routes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Domain < sorted[j].Domain })

	byDomain := rulesByDomain(rules)

	var caddyRoutes []any
	for _, r := range sorted {
		applied := byDomain[r.Domain]
		for _, rule := range applied {
			if deny := ruleDenyRoute(r, rule); deny != nil {
				caddyRoutes = append(caddyRoutes, deny)
			}
		}
		if deny := denyRoute(r); deny != nil {
			caddyRoutes = append(caddyRoutes, deny)
		}
		caddyRoutes = append(caddyRoutes, proxyRoute(r, applied, pol, opt))
	}
	if caddyRoutes == nil {
		caddyRoutes = []any{}
	}

	servers := map[string]any{
		"edge": map[string]any{
			"listen":    []string{opt.HTTPListen},
			"protocols": serverProtocols(pol, false),
			"routes":    plainRoutes(caddyRoutes, pol),
			// 打开 server 级 metrics：回源率靠它算。
			// caddy_http_requests_total{handler="reverse_proxy"} 是到达
			// upstream 的请求数，其余 handler 的是被边缘拦下的
			// （api-contract §3）。不开这个就只能编一个数字。
			"metrics": map[string]any{},
			// 关掉自动 HTTPS：证书由主控集中签发并内联下发（ADR-0001 / ADR-0010），
			// 开着会让节点自己去 ACME 申请——而它既没有 DNS 凭据，
			// 也不该有。
			"automatic_https": map[string]any{"disable": true},
		},
	}

	cfg := map[string]any{
		"apps": map[string]any{
			"http": map[string]any{"servers": servers},
		},
		// 日志与响应头由全局策略决定。**渲染它是必需的**——一条能改、能进
		// 资源树、有版本号却对节点毫无影响的设置，比没有这个设置更糟。
		"logging": loggingApp(pol),
	}

	// **只在主控持有证书时才渲染 apps/tls 与 :443**（ADR-0010）。
	//
	// 一张证书都没有时完全不渲染这个 app，节点上外部证书平台写入的内容原样保留。
	// 反过来的话，一个还没签发证书的系统会把那些内容抹掉——那是上一版真出过的事故。
	//
	// :443 同理：一个没有证书的 TLS 监听会让每一次握手都失败，比不监听更糟。
	if len(certs) > 0 {
		servers["edge_tls"] = map[string]any{
			"listen":          []string{opt.HTTPSListen},
			"routes":          caddyRoutes,
			"metrics":         map[string]any{},
			"automatic_https": map[string]any{"disable": true},
			// 空的连接策略让**这台 server** 转 TLS。它只加在 :443 那台上——
			// 加到 :80 那台会让所有没有服务端证书的域名立即失联（ADR-0010 实测）。
			// 空的连接策略让**这台 server** 转 TLS。它只加在 :443 那台上——
			// 加到 :80 那台会让所有没有服务端证书的域名立即失联（ADR-0010 实测）。
			"tls_connection_policies": []any{map[string]any{
				"protocol_min": tlsProtocolMin(pol.TLS.MinVersion),
			}},
		}
		cfg["apps"].(map[string]any)["tls"] = tlsApp(certs)
	}

	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		// map[string]any 里全是可序列化的类型，走到这里说明代码写错了。
		panic(fmt.Sprintf("渲染 Caddy 配置失败: %v", err))
	}
	return b, nil
}

// VerifyRules 挑出需要 Agent 校验端点验签的规则，连同密钥一起。
//
// **产出与 Render 分开**：这份东西带密钥，走隧道旁路送给 Agent，
// 绝不进 Caddy 配置（Admin API 能读回整份运行配置）。分成两个函数是为了让
// 「哪份带密钥」在调用处一眼可见，而不是靠记住某个字段。
func VerifyRules(rules []model.Rule) []model.VerifyRule {
	var out []model.VerifyRule
	sorted := append([]model.Rule(nil), rules...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, r := range sorted {
		if !r.Enabled || len(r.ApplyTo) == 0 {
			continue
		}
		switch r.Type {
		case model.RuleServiceSecret:
			out = append(out, model.VerifyRule{
				ID: r.ID, Type: r.Type, Header: r.Spec.Header,
				TTLSec: r.Spec.TTLSeconds, Replay: r.Spec.ReplayProtection, Secret: r.Secret,
			})
		case model.RuleJWTBearer:
			out = append(out, model.VerifyRule{
				ID: r.ID, Type: r.Type, Issuer: r.Spec.Issuer, Audience: r.Spec.Audience,
				JWKSURL: r.Spec.JWKSURL, SkewSec: r.Spec.SkewSeconds,
			})
		}
	}
	return out
}

// plainRoutes / tlsRoutes 在路由前面挂上响应头处理。
//
// 分成两个是因为 HSTS 只在 TLS 上有意义：在明文响应里发它，浏览器会忽略，
// 而它会让人以为已经生效了。
func plainRoutes(routes []any, pol Policies) []any { return withHeaders(routes, pol, false) }
func tlsRoutes(routes []any, pol Policies) []any   { return withHeaders(routes, pol, true) }

func withHeaders(routes []any, pol Policies, tls bool) []any {
	h := responseHeaderHandler(pol, tls)
	if h == nil {
		return routes
	}
	// 不带 match，也不 terminal：它对所有请求生效，然后让请求继续往下走。
	out := make([]any, 0, len(routes)+1)
	out = append(out, map[string]any{"handle": []any{h}})
	return append(out, routes...)
}

// tlsApp 用 load_pem 把证书**内联**进配置（ADR-0010）。
//
// 不用 load_files 落盘：落盘要求主控渲染的路径与节点上的实际路径一致，
// 而那是两个进程各自持有的知识，迟早会不一致。内联让配置自带全部内容，
// 没有第二处需要对齐。
//
// 代价是私钥出现在 Caddy 的运行配置里，能通过 Admin API 读到。Admin 只监听
// 回环，且能访问它的人本来就等于拥有这台节点。**但这个论证不覆盖浏览器**——
// 所以确认弹层的权威 diff 排除 apps/tls（ADR-0007 补充）。
func tlsApp(certs []Cert) map[string]any {
	sorted := append([]Cert(nil), certs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Domain < sorted[j].Domain })

	pairs := make([]any, 0, len(sorted))
	for _, c := range sorted {
		pairs = append(pairs, map[string]any{
			"certificate": string(c.CertPEM),
			"key":         string(c.KeyPEM),
			"tags":        []string{c.Domain},
		})
	}
	return map[string]any{
		"certificates": map[string]any{"load_pem": pairs},
	}
}

// denyRoute 是白名单之外流量的处置。放在代理路由**之前**，命中即终止。
// 白名单为空表示不限制，返回 nil。
func denyRoute(r model.Route) map[string]any {
	if len(r.Whitelist) == 0 {
		return nil
	}
	return map[string]any{
		"match": []any{map[string]any{
			"host": []string{r.Domain},
			"not": []any{map[string]any{
				"remote_ip": map[string]any{"ranges": normalizeRanges(r.Whitelist)},
			}},
		}},
		"handle":   []any{blockHandler(r.BlockMode)},
		"terminal": true,
	}
}

// rulesByDomain 把生效的规则按域名归拢。
//
// **未绑定域名的规则不生效**，停用的也不生效——两者都不是「对所有域名生效」，
// 而是半成品或被明确关掉的状态（CONTEXT.md「访问规则」）。
func rulesByDomain(rules []model.Rule) map[string][]model.Rule {
	out := map[string][]model.Rule{}
	sorted := append([]model.Rule(nil), rules...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, rule := range sorted {
		if !rule.Enabled {
			continue
		}
		for _, d := range rule.ApplyTo {
			out[d] = append(out[d], rule)
		}
	}
	return out
}

// ruleDenyRoute 处理 IP 白名单类规则。
//
// 它**不走校验端点**：Caddy 的 remote_ip 匹配器原生就能做，绕一趟回环
// 只会给每个请求加一次 HTTP 调用，换不到任何东西。
func ruleDenyRoute(r model.Route, rule model.Rule) map[string]any {
	if rule.Type != model.RuleIPWhitelist || len(rule.Spec.IPs) == 0 {
		return nil
	}
	return map[string]any{
		"match": []any{map[string]any{
			"host": []string{r.Domain},
			"not": []any{map[string]any{
				"remote_ip": map[string]any{"ranges": normalizeRanges(rule.Spec.IPs)},
			}},
		}},
		"handle":   []any{blockHandler(r.BlockMode)},
		"terminal": true,
	}
}

// forwardAuthHandler 把一次准入判断委托给 Agent 的校验端点。
//
// 它必须与 reverse_proxy 处在**同一条 handle 链**里，不能是独立路由：
// handle_response 匹配到 2xx 后请求会继续走本链的下一个 handler，
// 拆成两条路由就断了，校验通过之后请求到不了代理。
//
// 规则 id 走**路径**而不是请求头：请求头客户端可以伪造，路径是主控渲染进
// Caddy 配置里的，客户端碰不到。
func forwardAuthHandler(rule model.Rule, opt Options) map[string]any {
	return map[string]any{
		"handler":   "reverse_proxy",
		"upstreams": []any{map[string]any{"dial": opt.VerifyAddr}},
		"rewrite": map[string]any{
			"method": "GET",
			"uri":    "/verify/" + rule.ID,
		},
		"headers": map[string]any{
			"request": map[string]any{
				"set": map[string]any{
					// 把原请求的方法与 URI 带过去：服务密钥的签名把它们纳入了
					// 计算，不带过去就无法校验，一条截获的签名也能换到别的路径上。
					"X-Forwarded-Method": []string{"{http.request.method}"},
					"X-Forwarded-Uri":    []string{"{http.request.uri}"},
				},
			},
		},
		"handle_response": []any{map[string]any{
			"match": map[string]any{"status_code": []int{2}},
			"routes": []any{map[string]any{
				"handle": []any{map[string]any{
					"handler": "headers",
					"request": map[string]any{
						"set": map[string]any{
							// 验签结果透传给源站：源站不必重新解析 token。
							// 这是「边缘只做格式过滤」那个方案给不了的（ADR-0003 实测）。
							"X-Verified-Sub":  []string{"{http.reverse_proxy.header.X-Verified-Sub}"},
							"X-Verified-Rule": []string{"{http.reverse_proxy.header.X-Verified-Rule}"},
						},
					},
				}},
			}},
		}},
	}
}

func blockHandler(mode string) map[string]any {
	switch mode {
	case model.Block403:
		return map[string]any{"handler": "static_response", "status_code": 403}
	case model.Block404:
		return map[string]any{"handler": "static_response", "status_code": 404}
	default:
		// 静默断连：不返回任何响应，连服务是否存在都不暴露。
		return map[string]any{"handler": "static_response", "abort": true}
	}
}

func proxyRoute(r model.Route, rules []model.Rule, pol Policies, opt Options) map[string]any {
	var handlers []any

	// 先准入后转发。JWT 与服务密钥走校验端点；IP 白名单已经在前面的 deny 路由
	// 里处理掉了，不重复。
	for _, rule := range rules {
		switch rule.Type {
		case model.RuleServiceSecret, model.RuleJWTBearer:
			handlers = append(handlers, forwardAuthHandler(rule, opt))
		}
	}

	if n, ok := parseBodyMax(r.BodyMax); ok && n > 0 {
		// max_size 要的是 int64 字节数，不是 "5MB" 这种字符串——原样下发会被整份拒绝。
		// 这个转换只有这一处（ADR-0007 举的正是这个例子）。
		handlers = append(handlers, map[string]any{
			"handler":  "request_body",
			"max_size": n,
		})
	}
	if r.Compress {
		handlers = append(handlers, map[string]any{
			"handler":   "encode",
			"encodings": map[string]any{"gzip": map[string]any{}},
		})
	}
	proxy := map[string]any{
		"handler":   "reverse_proxy",
		"upstreams": []any{map[string]any{"dial": r.Upstream}},
	}
	if r.MTLS {
		// **回源 mTLS**：边缘节点作为客户端，向源站证明自己的身份（ADR-0008）。
		// 不是「要求访问者出示证书」——两者方向相反。
		//
		// 渲染成 reverse_proxy.transport.tls，**不碰 tls_connection_policies**：
		// 那会让整台 server 转 TLS，同节点上所有没有服务端证书的域名会立即失联。
		//
		// 用 client_certificate_file 而不是设计稿的 client_certificate_automate：
		// 后者要求每台节点持有 CA 私钥，且 6 台节点会各自成为独立的 CA，
		// 源站得同时信任 6 个根。改由主控持根、经隧道下发叶子（ADR-0009）。
		// 字段在 transport.**tls 里面**，不是 transport 上。
		// 第一版写成了 transport 上的 tls_client_certificate_file，
		// golden 快照照样通过——只有真 Caddy 说得出「这份配置我不接受」。
		// ADR-0004 承认的那个盲区就是指这个。
		proxy["transport"] = map[string]any{
			"protocol": "http",
			"tls": map[string]any{
				"client_certificate_file":     opt.UpstreamClientCert,
				"client_certificate_key_file": opt.UpstreamClientKey,
			},
		}
	}
	handlers = append(handlers, proxy)

	return map[string]any{
		"match":    []any{map[string]any{"host": []string{r.Domain}}},
		"handle":   handlers,
		"terminal": true,
	}
}

// normalizeRanges 把裸 IP 补成 /32 或 /128。Caddy 的 remote_ip 接受两种写法，
// 但统一成 CIDR 让渲染产出稳定，diff 里不会因为写法差异跳行。
func normalizeRanges(list []string) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			out = append(out, e)
			continue
		}
		if ip := net.ParseIP(e); ip != nil {
			if ip.To4() != nil {
				out = append(out, e+"/32")
			} else {
				out = append(out, e+"/128")
			}
			continue
		}
		out = append(out, e)
	}
	return out
}

var domainRE = regexp.MustCompile(`^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

// Validate 是下发前的 Go 层校验。返回**全部**问题，不是第一个。
func Validate(routes []model.Route, rules []model.Rule) []Issue {
	var issues []Issue
	seen := map[string]bool{}
	domains := map[string]bool{}
	for _, r := range routes {
		domains[r.Domain] = true
	}

	for _, r := range routes {
		key := "route:" + r.Domain

		switch {
		case r.Domain == "":
			issues = append(issues, Issue{key, "domain", "域名不能为空"})
		case !domainRE.MatchString(r.Domain):
			issues = append(issues, Issue{key, "domain", "不是合法的域名"})
		case seen[r.Domain]:
			issues = append(issues, Issue{key, "domain", "域名重复"})
		}
		seen[r.Domain] = true

		if err := validateUpstream(r.Upstream); err != nil {
			issues = append(issues, Issue{key, "upstream", err.Error()})
		}

		switch r.BlockMode {
		case model.BlockAbort, model.Block403, model.Block404, "":
		default:
			issues = append(issues, Issue{key, "block_mode",
				fmt.Sprintf("处置方式只能是 %s / %s / %s", model.BlockAbort, model.Block403, model.Block404)})
		}

		if r.BodyMax != "" {
			if _, ok := parseBodyMax(r.BodyMax); !ok {
				issues = append(issues, Issue{key, "body_max", "看不懂的大小，形如 5MB / 512KB / 1GB"})
			}
		}

		for i, e := range r.Whitelist {
			if !validCIDROrIP(e) {
				issues = append(issues, Issue{key,
					fmt.Sprintf("whitelist[%d]", i), fmt.Sprintf("%q 不是合法的 IP 或 CIDR", e)})
			}
		}

	}

	issues = append(issues, validateRules(rules, domains)...)
	return issues
}

func validateRules(rules []model.Rule, domains map[string]bool) []Issue {
	var issues []Issue
	seen := map[string]bool{}

	for _, rule := range rules {
		key := "rule:" + rule.ID

		// **只校验真正会被渲染的规则。** 停用的、以及未绑定域名的都不生效
		// （CONTEXT.md「访问规则」：那是半成品状态，不是「对所有域名生效」）。
		// 校验它们会让一条还没填完的规则挡住全站的下发——而人本来就是
		// 「先建规则、再慢慢配」的顺序。
		if !rule.Enabled || len(rule.ApplyTo) == 0 {
			continue
		}

		if rule.ID == "" {
			issues = append(issues, Issue{key, "id", "规则标识不能为空"})
		} else if seen[rule.ID] {
			issues = append(issues, Issue{key, "id", "规则标识重复"})
		}
		seen[rule.ID] = true

		for i, d := range rule.ApplyTo {
			if !domains[d] {
				// 绑到一个不存在的域名上不是「不生效」那么简单——它会让人
				// 以为某个域名受保护，而那个域名根本没有路由。
				issues = append(issues, Issue{key,
					fmt.Sprintf("apply_to[%d]", i), fmt.Sprintf("没有 %s 这条路由", d)})
			}
		}

		switch rule.Type {
		case model.RuleIPWhitelist:
			if len(rule.Spec.IPs) == 0 {
				issues = append(issues, Issue{key, "spec.ips", "IP 白名单不能为空"})
			}
			for i, e := range rule.Spec.IPs {
				if !validCIDROrIP(e) {
					issues = append(issues, Issue{key,
						fmt.Sprintf("spec.ips[%d]", i), fmt.Sprintf("%q 不是合法的 IP 或 CIDR", e)})
				}
			}

		case model.RuleServiceSecret:
			if rule.Spec.Header == "" {
				issues = append(issues, Issue{key, "spec.header", "请求头名称不能为空"})
			}
			if rule.Spec.Algo != "" && rule.Spec.Algo != "hmac-sha256" {
				issues = append(issues, Issue{key, "spec.algo", "目前只支持 hmac-sha256"})
			}
			if rule.Spec.TTLSeconds <= 0 {
				issues = append(issues, Issue{key, "spec.ttl_s", "时间窗口必须为正"})
			}
			if rule.Secret == "" {
				// 没有密钥的服务密钥规则会让校验端点无条件拒绝，
				// 表现为「这个域名整体 403」——而配置看起来完全正常。
				// 字段路径是 **secret**（顶层）而不是 spec.secret：密钥不在 spec 里
				// ——放进去就等于被 GET /rules 回显了。
				//
				// 契约 §0.3 说前端按这个点号路径去索引表单字段。指向一个请求体里
				// 不存在的路径，这条错误就会掉在地上，只剩一个笼统的「未通过校验」
				// ——**一条指不到地方的错误信息，等于没有这条错误信息。**
				issues = append(issues, Issue{key, "secret", "尚未设置共享密钥"})
			}

		case model.RuleJWTBearer:
			if rule.Spec.JWKSURL == "" {
				issues = append(issues, Issue{key, "spec.jwks_url", "JWKS 地址不能为空"})
			}
			if rule.Spec.SkewSeconds < 0 {
				issues = append(issues, Issue{key, "spec.skew_s", "时钟偏移不能为负"})
			}

		default:
			issues = append(issues, Issue{key, "type",
				fmt.Sprintf("未知的规则类型 %q", rule.Type)})
		}
	}
	return issues
}

func validateUpstream(u string) error {
	if u == "" {
		return fmt.Errorf("回源地址不能为空")
	}
	host, port, err := net.SplitHostPort(u)
	if err != nil {
		return fmt.Errorf("回源地址必须形如 host:port")
	}
	if host == "" {
		return fmt.Errorf("回源地址缺少主机部分")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("端口必须是 1-65535 之间的整数")
	}
	return nil
}

func validCIDROrIP(e string) bool {
	e = strings.TrimSpace(e)
	if e == "" {
		return false
	}
	if strings.Contains(e, "/") {
		_, _, err := net.ParseCIDR(e)
		return err == nil
	}
	return net.ParseIP(e) != nil
}

var bodyMaxRE = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)\s*([KMGT]?B?)\s*$`)

// parseBodyMax 用 1024 进制。歧义无法避免（"MB" 在不同工具里各有含义），
// 选二进制是因为它与 Caddy 文档和运维直觉一致；这个选择写在这里，
// 而不是散落在调用方的猜测里。
func parseBodyMax(s string) (int64, bool) {
	m := bodyMaxRE.FindStringSubmatch(strings.ToUpper(s))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	mult := int64(1)
	switch strings.TrimSuffix(m[2], "B") {
	case "K":
		mult = 1 << 10
	case "M":
		mult = 1 << 20
	case "G":
		mult = 1 << 30
	case "T":
		mult = 1 << 40
	}
	return int64(v * float64(mult)), true
}
