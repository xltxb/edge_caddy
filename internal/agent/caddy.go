package agent

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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

	// apps 键不存在时，POST /config/apps/<name> 会失败：实测（Caddy 2.11.4）
	// 返回 500 `invalid traversal path at: config/apps/http`——那句话既不说
	// 是哪一步出了问题，也不提示该怎么办。
	//
	// 一台刚装完官方包的机器上，Caddy 跑的是 /etc/caddy/Caddyfile；只要那个
	// 文件是空的（或被清过），apps 就不存在，而那正是干净机器最可能的状态。
	if err := c.ensureAppsExists(ctx); err != nil {
		return 0, err
	}

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

// ensureAppsExists 保证 config 里有 apps 键，没有就建一个空的。
//
// 用 PUT 而不是 POST：POST 到已存在的键会**替换**它，把节点上其它 app 抹掉；
// 而这里的意图恰恰是「只在缺的时候补上」。实测 PUT 到已存在的键会返回错误，
// 因此先读一次再决定。
func (c *CaddyClient) ensureAppsExists(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.adminAddr+"/config/", nil)
	if err != nil {
		return fmt.Errorf("构造请求: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("读取 caddy 当前配置: %w", err)
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("读取 caddy 当前配置失败（HTTP %d）: %s",
			resp.StatusCode, strings.TrimSpace(string(blob)))
	}

	var cur map[string]json.RawMessage
	// 配置为 null（全新实例）时 Unmarshal 会得到 nil map，正是我们要补的情形
	if err := json.Unmarshal(blob, &cur); err != nil {
		return fmt.Errorf("解析 caddy 当前配置: %w", err)
	}
	if _, has := cur["apps"]; has {
		return nil
	}

	put, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.adminAddr+"/config/apps", strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("构造请求: %w", err)
	}
	put.Header.Set("Content-Type", "application/json")
	pr, err := c.http.Do(put)
	if err != nil {
		return fmt.Errorf("初始化 caddy apps: %w", err)
	}
	defer pr.Body.Close()
	pb, _ := io.ReadAll(io.LimitReader(pr.Body, 4096))
	if pr.StatusCode != http.StatusOK {
		return fmt.Errorf("初始化 caddy apps 失败（HTTP %d）: %s",
			pr.StatusCode, strings.TrimSpace(string(pb)))
	}
	return nil
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

// LoadedCert 是节点上 Caddy 实际加载着的一张证书。
type LoadedCert struct {
	Domain   string
	NotAfter string
	Issuer   string
	KeyType  string
	Serial   string
}

// LoadedCerts 读回 Caddy 当前 tls app 里加载的证书。
//
// 从**运行中的配置**读，而不是从我们下发的那份算：两者不一致正是要发现的东西
// （下发失败、被人手工改过、或者 Caddy 拒绝了某一张）。从下发的那份算等于
// 自己给自己打分。
func (c *CaddyClient) LoadedCerts(ctx context.Context) ([]LoadedCert, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.adminAddr+"/config/apps/tls", nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取 caddy tls 配置: %w", err)
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		// 没有 tls app 不是错误：这台节点上还没有证书
		return nil, nil
	}

	var cfg struct {
		Certificates struct {
			LoadPEM []struct {
				Certificate string `json:"certificate"`
			} `json:"load_pem"`
		} `json:"certificates"`
	}
	if err := json.Unmarshal(blob, &cfg); err != nil {
		return nil, fmt.Errorf("解析 caddy tls 配置: %w", err)
	}

	out := make([]LoadedCert, 0, len(cfg.Certificates.LoadPEM))
	for _, entry := range cfg.Certificates.LoadPEM {
		block, _ := pem.Decode([]byte(entry.Certificate))
		if block == nil {
			continue
		}
		x, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		domain := x.Subject.CommonName
		if len(x.DNSNames) > 0 {
			domain = x.DNSNames[0]
		}
		out = append(out, LoadedCert{
			Domain:   domain,
			NotAfter: x.NotAfter.UTC().Format(time.RFC3339),
			Issuer:   x.Issuer.CommonName,
			KeyType:  x.PublicKeyAlgorithm.String() + " " + x.SignatureAlgorithm.String(),
			Serial:   x.SerialNumber.Text(16),
		})
	}
	return out, nil
}
