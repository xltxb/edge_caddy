// Package render 把领域资源渲染成 Caddy 配置。
//
// 这个包是**下发的唯一权威**（docs/adr/0007）：工作台右栏那份可读表示不是
// 下发内容，确认弹层和真正发给节点的都来自这里。
//
// 必须是纯函数：不做 IO、不持有状态、不读时钟、不依赖 map 遍历顺序。
// 相同输入必须产生逐字节相同的输出——否则 diff 会出现虚假变更，而 diff
// 正是「改配置怕推错」这个核心痛点的解药。
package render

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/dustin/go-humanize"
	"github.com/xltxb/edge_caddy/internal/model"
)

// Caddy 渲染 Caddy 配置文档的 apps 子树。
//
// 产物是 apps 的**值**（形如 {"http":{...}}），不是外面再包一层 {"apps":...}。
// Agent 会遍历这一层的每个 app 逐个 POST 到 /config/apps/<name>。
func Caddy(routes []model.Route) ([]byte, error) {
	return CaddyWith(routes, DefaultOptions())
}

// Options 是渲染的配置项。
type Options struct {
	// Listen 是边缘 server 的监听地址。生产是 :443；测试与本地调试需要
	// 非特权端口，因此它是配置而不是常量——写死会让「能不能跑通」这件事
	// 只能靠特权用户验证。
	Listen []string
}

func DefaultOptions() Options { return Options{Listen: []string{":443"}} }

// CaddyWith 用给定配置渲染。
func CaddyWith(routes []model.Route, opts Options) ([]byte, error) {
	if len(opts.Listen) == 0 {
		opts.Listen = DefaultOptions().Listen
	}
	sorted := make([]model.Route, len(routes))
	copy(sorted, routes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Domain < sorted[j].Domain })

	for i := 1; i < len(sorted); i++ {
		if sorted[i].Domain != "" && sorted[i].Domain == sorted[i-1].Domain {
			return nil, fmt.Errorf("域名 %s 被多条路由重复占用（同一域名只能有一条）", sorted[i].Domain)
		}
	}

	httpRoutes := make([]any, 0, len(sorted))
	for _, r := range sorted {
		hr, err := renderRoute(r)
		if err != nil {
			return nil, err
		}
		httpRoutes = append(httpRoutes, hr)
	}

	apps := map[string]any{
		"http": map[string]any{
			"servers": map[string]any{
				"edge": map[string]any{
					"listen": opts.Listen,
					// 证书由主控签发后经隧道下发（docs/adr/0001），节点不得自行申请：
					// 少了这一段，节点会去 ACME 申请并可能触发速率限制。
					"automatic_https": map[string]any{"disable": true},
					"routes":          httpRoutes,
				},
			},
		},
	}

	out, err := json.MarshalIndent(apps, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化 Caddy 配置: %w", err)
	}
	return out, nil
}

func renderRoute(r model.Route) (map[string]any, error) {
	if r.Domain == "" {
		return nil, fmt.Errorf("路由的域名为空")
	}
	if r.Upstream == "" {
		return nil, fmt.Errorf("路由 %s 的回源地址为空", r.Domain)
	}
	if !r.Block.Valid() {
		return nil, fmt.Errorf("路由 %s 的处置方式 %q 不是 abort / 403 / 404", r.Domain, r.Block)
	}

	branches := make([]any, 0, 2)
	if guard := renderWhitelistGuard(r); guard != nil {
		branches = append(branches, guard)
	}

	handlers := make([]any, 0, 3)
	if r.Compress {
		handlers = append(handlers, map[string]any{
			"handler":   "encode",
			"encodings": map[string]any{"zstd": map[string]any{}, "gzip": map[string]any{}},
		})
	}

	size, err := parseBodyMax(r)
	if err != nil {
		return nil, err
	}
	handlers = append(handlers, map[string]any{"handler": "request_body", "max_size": size})
	handlers = append(handlers, renderProxy(r))

	branches = append(branches, map[string]any{"handle": handlers})

	return map[string]any{
		"match":    []any{map[string]any{"host": []string{r.Domain}}},
		"handle":   []any{map[string]any{"handler": "subroute", "routes": branches}},
		"terminal": true,
	}, nil
}

// parseBodyMax 把 "5MB" 转成字节数。
//
// 用 humanize.ParseBytes 而不是自己写：Caddyfile 层解析同样的值时用的就是
// 这个库，保证 MB（10^6）与 MiB（2^20）的语义在两条路径上不漂移。
func parseBodyMax(r model.Route) (int64, error) {
	if r.BodyMax == "" {
		return 0, fmt.Errorf("路由 %s 的请求体上限为空", r.Domain)
	}
	n, err := humanize.ParseBytes(r.BodyMax)
	if err != nil {
		return 0, fmt.Errorf("路由 %s 的请求体上限 %q 不是合法的大小: %w", r.Domain, r.BodyMax, err)
	}
	// ParseBytes 只在 >= MaxUint64 时报错，不管是否超过 MaxInt64。
	// 超过时 int64(n) 会静默环绕成负数，把负的 max_size 发给 Caddy
	// 属于静默数据损坏，必须在这里拦下。
	if n > math.MaxInt64 {
		return 0, fmt.Errorf("路由 %s 的请求体上限 %q 超出上限（最大 8EB）", r.Domain, r.BodyMax)
	}
	return int64(n), nil
}

// renderWhitelistGuard 渲染「不在白名单就拒绝」的分支，无需拒绝时返回 nil。
func renderWhitelistGuard(r model.Route) map[string]any {
	wl := NormalizeWhitelist(r.Whitelist)
	// 空白名单不渲染拒绝分支：渲染成「谁都不许进」会把一条半配置好的
	// 路由变成一次全站故障。
	if len(wl) == 0 {
		return nil
	}
	// 只有 0.0.0.0/0 等于放行所有来源。渲染一个「谁都匹配」的 not 匹配器
	// 与不渲染等价，但会在 diff 里留下无意义的噪声。
	if len(wl) == 1 && wl[0] == model.AllowAllCIDR {
		return nil
	}
	return map[string]any{
		"match":    []any{map[string]any{"not": []any{map[string]any{"remote_ip": map[string]any{"ranges": wl}}}}},
		"handle":   []any{denyHandler(r.Block)},
		"terminal": true,
	}
}

// NormalizeWhitelist 去掉首尾空白与空项。
//
// 导出是因为草稿比对要用同一套规范化：用户把白名单文本框里的换行删了又加回来，
// 值其实没变，不能算作一处待下发的改动（对应设计稿的 normWL / sameVal）。
func NormalizeWhitelist(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := trimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// denyHandler 把处置方式渲染成响应处置。
func denyHandler(b model.BlockAction) map[string]any {
	switch b {
	case model.Block403:
		return map[string]any{"handler": "static_response", "status_code": 403}
	case model.Block404:
		return map[string]any{"handler": "static_response", "status_code": 404}
	default:
		return map[string]any{"handler": "static_response", "abort": true}
	}
}

// renderProxy 渲染回源。
func renderProxy(r model.Route) map[string]any {
	transport := map[string]any{
		"protocol": "http",
		"keep_alive": map[string]any{
			"max_idle_conns_per_host": 100,
			"idle_timeout":            60_000_000_000, // 60s，Caddy 的 Duration 以纳秒计
		},
	}
	if r.MTLS {
		// 回源 mTLS：边缘节点向源站出示客户端证书（docs/adr/0008）。
		// 用 client_certificate_file 而非设计稿的 client_certificate_automate——
		// 后者由节点本机的 Caddy 当 CA 签发，会让每台节点各成一个根、
		// 且每台都存着 CA 私钥（docs/adr/0009）。证书由主控签发后下发到此路径。
		transport["tls"] = map[string]any{
			"client_certificate_file":     UpstreamCertPath,
			"client_certificate_key_file": UpstreamKeyPath,
		}
	}
	return map[string]any{
		"handler":   "reverse_proxy",
		"upstreams": []any{map[string]any{"dial": r.Upstream}},
		"transport": transport,
		"headers": map[string]any{
			"request": map[string]any{
				"set": map[string]any{
					"Host":              []string{"{http.reverse_proxy.upstream.hostport}"},
					"X-Real-IP":         []string{"{http.request.remote.host}"},
					"X-Forwarded-Proto": []string{"{http.request.scheme}"},
				},
			},
		},
	}
}

// 回源客户端证书在节点上的落盘位置，由 Agent 写入并保持续期。
const (
	UpstreamCertPath = "/etc/edge-agent/pki/upstream.crt"
	UpstreamKeyPath  = "/etc/edge-agent/pki/upstream.key"
)
