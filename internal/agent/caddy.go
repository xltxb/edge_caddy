// Package agent 是边缘节点上的常驻进程。它不做决策，只执行与回报（CONTEXT.md）。
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// CaddyClient 托管本机 Caddy，只通过它的 Admin API 说话。
type CaddyClient struct {
	Admin string // 形如 http://127.0.0.1:2019
	HTTP  *http.Client
}

// NewCaddyClient 接受两种 Admin 地址：
//
//	http://127.0.0.1:2019       生产默认
//	unix//path/to/admin.sock    Caddy 的 unix socket 写法（前缀 unix/ + 绝对路径）
//
// 支持后者不只是为了测试：Admin 走 unix socket 时它根本不占端口，
// 也就不存在「谁都能连上回环 2019」这件事，姿态比 ADR-0010 里靠防火墙兜底更严。
//
// **不复用连接（DisableKeepAlives）。这不是保守，是这个端点的事实。**
//
// 实测：Caddy 每一次**写配置**都会重启 admin 监听。日志里是
//
//	PUT  /config/apps       → admin endpoint started
//	POST /config/apps/http  → admin endpoint started
//	POST /config/apps/pki   → admin endpoint started
//	                        → stopped previous server（异步，滞后）
//
// 也就是说这个客户端发出的每一个写请求都会让**自己刚用过的那条连接作废**，
// 连接池在这里从来就没有可复用的东西。而旧监听是异步关掉的，于是存在一个窗口：
// Go 从池里取出一条连接、正要写请求，那一头被关了——`Post ...: EOF`。
//
// 这个 EOF 在全量并行跑时撞见过两次，两次都是 ApplyConfig 里紧跟 putEmptyApps
// 的那个 POST。**而它是每一台全新机器首次下发必经的那一步**：空 Caddyfile
// 的机器没有 apps 键，一定会走 putEmptyApps。
//
// 顺序跑复现不出来（30 次 0 失败，把窗口人为拉宽到 80ms 也是 0 失败）——
// 因为窗口宽的时候 Go 反而能在取连接时就发现它已经死了，转而重新拨号。
// 真正会出事的是**恰好在取出之后、写入之前**被关掉，那需要机器负载够重。
//
// 所以修法不是重试。**重试会把「连不上」和「被拒绝」揉成一件事，
// 而 ADR-0005 的整套失败分类正建立在这两者的区分上。**
// 不复用连接则是把那个窗口整个去掉：每次都新拨一条，没有池子里的陈货可取。
// 代价是每个请求多一次 unix connect——一次下发也就几个请求。
func NewCaddyClient(admin string) *CaddyClient {
	c := &CaddyClient{Admin: admin, HTTP: &http.Client{Timeout: 10 * time.Second}}

	if path, ok := strings.CutPrefix(admin, "unix/"); ok {
		c.Admin = "http://caddy-admin" // 主机名只是占位，实际连的是 socket
		c.HTTP.Transport = &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			},
		}
		return c
	}
	c.HTTP.Transport = &http.Transport{DisableKeepAlives: true}
	return c
}

// Apply 把一个 app 的配置写进本机 Caddy，返回耗时。
//
// 耗时由这里测量而不是由主控推算：主控测到的是往返，含隧道延迟，
// 而控制台上那个「31ms」应当是节点上热重载真正花的时间。
//
// **POST 单个 app 之前先确认 apps 键存在。** 一台刚装完官方包、Caddyfile 为空的
// 机器，运行配置里根本没有 apps 键，直接 POST 会得到
// `500 invalid traversal path at: config/apps/http`。缺了就用 **PUT** 补一个空对象
// ——用 POST 会替换掉已存在的键，把别的 app 一起抹掉（ADR-0010）。
func (c *CaddyClient) Apply(ctx context.Context, app string, body []byte) (time.Duration, error) {
	start := time.Now()

	ok, err := c.hasAppsKey(ctx)
	if err != nil {
		return 0, err
	}
	if !ok {
		if err := c.putEmptyApps(ctx); err != nil {
			return 0, err
		}
	}

	if err := c.postConfig(ctx, "apps/"+app, body); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// postConfig 把一段配置 POST 到 /config/ 下的某个路径。
func (c *CaddyClient) postConfig(ctx context.Context, path string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Admin+"/config/"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// 连不上 Admin —— Caddy 挂了或还没起来。这与「Caddy 拒绝了配置」
		// 是两种完全不同的故障，调用方据此决定要不要重试（ADR-0005）。
		return fmt.Errorf("连接 Caddy Admin: %w", err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 原文回报，不做归类。Caddy 对各种坏配置一律返回 500，措辞才是有信息的
		// 那部分；把它翻译成我们自己的话只会丢掉排查线索（ADR-0005）。
		return &RejectedError{Status: resp.StatusCode, Body: trimJSONError(msg)}
	}
	return nil
}

// RejectedError 表示节点回应了，但 Caddy 拒绝了这份配置。
//
// 与「连不上」区分开是 ADR-0005 的全部要点：同一份字节喂给同一个 Caddy 必然
// 得到同样的拒绝，重试对它无效——能修它的是人改配置，不是时间。
type RejectedError struct {
	Status int
	Body   string
}

func (e *RejectedError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("Caddy 拒绝了配置（HTTP %d）", e.Status)
	}
	return e.Body
}

func (c *CaddyClient) hasAppsKey(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Admin+"/config/", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("读取 Caddy 当前配置: %w", err)
	}
	defer resp.Body.Close()

	var cfg map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		// 全新的 Caddy 在没有配置时会返回 null，解不出对象。当作「没有 apps 键」。
		return false, nil
	}
	_, ok := cfg["apps"]
	return ok, nil
}

func (c *CaddyClient) putEmptyApps(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.Admin+"/config/apps", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("补 apps 键: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("补 apps 键失败（HTTP %d）: %s", resp.StatusCode, trimJSONError(b))
	}
	return nil
}

// trimJSONError 把 Caddy 的 {"error":"..."} 剥成里面那句话。
// 保留原文措辞，只去掉包裹——那句话是排查时唯一有用的东西。
func trimJSONError(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return e.Error
	}
	return string(bytes.TrimSpace(b))
}

// ApplyConfig 应用主控渲染的**整份**配置，逐段 POST 下去，返回总耗时。
//
// 逐段而不是 POST /config/ 整体替换：整体替换会连 admin 段一起换掉，
// 而 admin 的监听地址是节点本地的事，不该由主控的渲染结果决定。
//
// **顶层的 logging 键也要应用。** 它是 apps 的兄弟，不在 apps 底下——
// 只遍历 apps 会把它静默丢掉，于是日志策略（格式、级别、file writer）
// 在控制台上能改、能下发、显示成功，而节点上什么也没变。
// 这正是「/var/log/caddy 永远是空的」的第二半根因：第一半是渲染器
// 没配 writer，配上之后还得真的送到节点才算数。
func (c *CaddyClient) ApplyConfig(ctx context.Context, full []byte) (time.Duration, error) {
	var cfg struct {
		Apps    map[string]json.RawMessage `json:"apps"`
		Logging json.RawMessage            `json:"logging"`
	}
	if err := json.Unmarshal(full, &cfg); err != nil {
		return 0, fmt.Errorf("主控下发的配置不是合法 JSON: %w", err)
	}
	if len(cfg.Apps) == 0 {
		return 0, fmt.Errorf("主控下发的配置里没有任何 app")
	}

	start := time.Now()
	// logging 先于 apps：server 一起来，访问行就该进文件，
	// 反过来的话头几行会落在旧的出口（stderr）里。
	// logging 是根下的直接子键，不需要像 apps/<name> 那样先补父键。
	if len(cfg.Logging) > 0 {
		if err := c.postConfig(ctx, "logging", cfg.Logging); err != nil {
			return 0, err
		}
	}
	// 排序后应用，让同一份配置每次的应用顺序一致——顺序不定会让偶发失败难以复现。
	for _, name := range sortedKeys(cfg.Apps) {
		if _, err := c.Apply(ctx, name, cfg.Apps[name]); err != nil {
			return 0, err
		}
	}
	return time.Since(start), nil
}

func sortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Config 读本机 Caddy **此刻在跑的**整份配置。
//
// 用途是 Agent 重启之后找回边缘端口：那份配置 Agent 自己没有留底，
// 而 Caddy 手里的才是真在生效的那一份。守着这条的是
// TestEdgePortsRecoveredFromRunningCaddyWithoutPush。
func (c *CaddyClient) Config(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Admin+"/config/", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取 Caddy 当前配置: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("读取 Caddy 当前配置: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// Alive 探一下本机 Caddy Admin 是否可达。
func (c *CaddyClient) Alive(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Admin+"/config/", nil)
	if err != nil {
		return false
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode >= 200 && resp.StatusCode < 500
}
