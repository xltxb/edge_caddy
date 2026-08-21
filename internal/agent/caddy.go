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
func NewCaddyClient(admin string) *CaddyClient {
	c := &CaddyClient{Admin: admin, HTTP: &http.Client{Timeout: 10 * time.Second}}

	if path, ok := strings.CutPrefix(admin, "unix/"); ok {
		c.Admin = "http://caddy-admin" // 主机名只是占位，实际连的是 socket
		c.HTTP.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			},
		}
	}
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Admin+"/config/apps/"+app, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// 连不上 Admin —— Caddy 挂了或还没起来。这与「Caddy 拒绝了配置」
		// 是两种完全不同的故障，调用方据此决定要不要重试（ADR-0005）。
		return 0, fmt.Errorf("连接 Caddy Admin: %w", err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 原文回报，不做归类。Caddy 对各种坏配置一律返回 500，措辞才是有信息的
		// 那部分；把它翻译成我们自己的话只会丢掉排查线索（ADR-0005）。
		return 0, &RejectedError{Status: resp.StatusCode, Body: trimJSONError(msg)}
	}
	return time.Since(start), nil
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

// ApplyConfig 应用主控渲染的**整份**配置，逐个 app POST 下去，返回总耗时。
//
// 逐 app 而不是 POST /config/ 整体替换：整体替换会连 admin 段一起换掉，
// 而 admin 的监听地址是节点本地的事，不该由主控的渲染结果决定。
func (c *CaddyClient) ApplyConfig(ctx context.Context, full []byte) (time.Duration, error) {
	var cfg struct {
		Apps map[string]json.RawMessage `json:"apps"`
	}
	if err := json.Unmarshal(full, &cfg); err != nil {
		return 0, fmt.Errorf("主控下发的配置不是合法 JSON: %w", err)
	}
	if len(cfg.Apps) == 0 {
		return 0, fmt.Errorf("主控下发的配置里没有任何 app")
	}

	start := time.Now()
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
