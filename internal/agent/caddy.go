package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// CaddyClient 是本机 Caddy Admin API 的最小客户端。
type CaddyClient struct {
	adminAddr string
	http      *http.Client
}

// NewCaddyClient 创建指向 Caddy Admin API 的客户端，addr 形如 http://127.0.0.1:2019。
func NewCaddyClient(addr string) *CaddyClient {
	return &CaddyClient{
		adminAddr: strings.TrimRight(addr, "/"),
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Apply 把渲染出的 apps 子树逐个 app 下发给 Caddy，返回总耗时毫秒数。
//
// 【不要改成整体 POST /config/apps】——那会用载荷**整体替换** apps。
// 我们只渲染 http，而节点上的 apps/tls 是外部证书平台写进去的，整体替换会把它
// 连同全部证书一起抹掉：每一次发布都让节点上所有 HTTPS 站点失去证书，而面板
// 这边看到的是一次成功发布，零反馈。实测（Caddy 2.11.4）先放好 apps/tls 再
// 整体 POST 一份只含 http 的载荷，apps 顶层键从 [http tls] 变成 [http]。
//
// 【也不要改成 POST /load】——/load 替换的是**整份**配置文档。我们的载荷不含
// admin 段，Caddy 会把 admin 重置成内置默认地址并停掉原有监听器，Agent 亲手
// 切断自己的控制通道，此后每次下发都是 connection refused，只能上机器修。
func (c *CaddyClient) Apply(ctx context.Context, appsJSON []byte) (int, error) {
	var apps map[string]json.RawMessage
	if err := json.Unmarshal(appsJSON, &apps); err != nil {
		// 载荷来自我们自己的渲染器，解不开属于内部缺陷，不是 Caddy 拒绝
		return 0, fmt.Errorf("解析 apps 载荷: %w", err)
	}
	if len(apps) == 0 {
		// 空载荷会把节点上正在跑的服务全部清掉——一次误操作变成一次全站中断
		return 0, fmt.Errorf("apps 载荷为空，拒绝下发")
	}

	// 键序固定，便于复现问题时比对日志
	names := make([]string, 0, len(apps))
	for name := range apps {
		names = append(names, name)
	}
	sort.Strings(names)

	total := 0
	for _, name := range names {
		ms, err := c.postApp(ctx, name, apps[name])
		total += ms
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (c *CaddyClient) postApp(ctx context.Context, name string, body json.RawMessage) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.adminAddr+"/config/apps/"+name, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("构造请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("请求 caddy admin: %w", err)
	}
	defer resp.Body.Close()
	ms := int(time.Since(start).Milliseconds())
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode != http.StatusOK {
		// 原文原样带回，不做归类：实测 Caddy 对语法错误、未知 handler、
		// 字段类型错、端口占用一律返回 500，任何基于状态码的归类都是错的
		// （docs/adr/0005）。排查时唯一有用的就是这段原文。
		return ms, fmt.Errorf("caddy 拒绝 apps/%s（HTTP %d）: %s",
			name, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return ms, nil
}

// Ping 探测本机 Caddy Admin 是否可达。
//
// 用 GET /config/ 而不是空请求：它既验证端口通，也验证 Caddy 真的在正常应答
// Admin API——进程活着但 Admin 挂了的情况下，前者会成功、只有后者能发现。
func (c *CaddyClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.adminAddr+"/config/", nil)
	if err != nil {
		return fmt.Errorf("构造探活请求: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("探活 caddy admin: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("caddy admin 返回 %d", resp.StatusCode)
	}
	return nil
}
