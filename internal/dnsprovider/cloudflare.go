package dnsprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CloudflareAPI 是官方 API v4 的入口。
const CloudflareAPI = "https://api.cloudflare.com/client/v4"

type CloudflareConfig struct {
	// APIToken 是 Cloudflare 的 API Token（不是 Global API Key）。
	//
	// Token 可以按 zone 和权限最小化授权；Global Key 等于整个账号的全部权限，
	// 一旦泄漏连账单都能改。这里只接受 Token。
	APIToken string
	// BaseURL 仅供测试指向本地服务端；为空时用官方地址。
	BaseURL string
	// HTTP 可替换，仅供测试。
	HTTP *http.Client
}

// Cloudflare 实现 Provider 的 TXT 部分。
//
// 线路与权重都不支持：Cloudflare 没有 ISP 线路概念，加权解析属于付费的
// Load Balancing。SupportsLines / SupportsWeights 如实返回 false，
// 调度那边据此拒绝，而不是悄悄按等权重写下去。
type Cloudflare struct {
	cfg  CloudflareConfig
	base string
	http *http.Client
}

func NewCloudflare(cfg CloudflareConfig) *Cloudflare {
	base := cfg.BaseURL
	if base == "" {
		base = CloudflareAPI
	}
	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Cloudflare{cfg: cfg, base: strings.TrimRight(base, "/"), http: hc}
}

func (c *Cloudflare) Name() string          { return "Cloudflare" }
func (c *Cloudflare) SupportsLines() bool   { return false }
func (c *Cloudflare) SupportsWeights() bool { return false }

func (c *Cloudflare) ListA(context.Context, string) ([]ARecord, error) {
	return nil, fmt.Errorf("%w：Cloudflare 没有线路与权重概念", ErrNotSupported)
}

func (c *Cloudflare) ApplyPlan(context.Context, string, []Target) error {
	return fmt.Errorf("%w：加权解析是 Cloudflare 的付费 Load Balancing", ErrNotSupported)
}

type cfEnvelope struct {
	Success bool              `json:"success"`
	Errors  []cfError         `json:"errors"`
	Result  json.RawMessage   `json:"result"`
	Info    map[string]any    `json:"result_info"`
	Msgs    []json.RawMessage `json:"messages"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cfDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// SetTXT 写一条 TXT 记录。
func (c *Cloudflare) SetTXT(ctx context.Context, fqdn, value string) error {
	zoneID, err := c.zoneOf(ctx, fqdn)
	if err != nil {
		return err
	}
	body := map[string]any{
		"type": "TXT", "name": fqdn, "content": value,
		// TTL 取 Cloudflare 允许的最小值：挑战记录只活几十秒，
		// 长 TTL 会让上一次的值在缓存里挡住这一次的校验。
		"ttl": 60,
	}
	_, err = c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", body)
	return err
}

// RemoveTXT 按 **名字 + 值** 精确删除。
//
// 同时给多个域名（或一个域名的多个 SAN）签发时，_acme-challenge 下会同时存在
// 多条 TXT。只按名字删会把别人的挑战记录一起删掉，让另一次签发在校验阶段
// 莫名失败——而现象是「偶发失败」，极难查。
func (c *Cloudflare) RemoveTXT(ctx context.Context, fqdn, value string) error {
	zoneID, err := c.zoneOf(ctx, fqdn)
	if err != nil {
		return err
	}
	q := url.Values{"type": {"TXT"}, "name": {fqdn}, "content": {value}}
	raw, err := c.do(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	var recs []cfDNSRecord
	if err := json.Unmarshal(raw, &recs); err != nil {
		return fmt.Errorf("解析 Cloudflare 记录列表: %w", err)
	}
	for _, rec := range recs {
		if rec.Content != value {
			continue
		}
		if _, err := c.do(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+rec.ID, nil); err != nil {
			return err
		}
	}
	return nil
}

// zoneOf 由完整域名找出所属 zone。
//
// 从最长后缀往短了试：_acme-challenge.a.example.com 的 zone 可能是
// a.example.com，也可能是 example.com，只有问过才知道。
func (c *Cloudflare) zoneOf(ctx context.Context, fqdn string) (string, error) {
	labels := strings.Split(strings.TrimSuffix(fqdn, "."), ".")
	for i := 0; i < len(labels)-1; i++ {
		candidate := strings.Join(labels[i:], ".")
		q := url.Values{"name": {candidate}}
		raw, err := c.do(ctx, http.MethodGet, "/zones?"+q.Encode(), nil)
		if err != nil {
			return "", err
		}
		var zones []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &zones); err != nil {
			return "", fmt.Errorf("解析 Cloudflare zone 列表: %w", err)
		}
		for _, z := range zones {
			if z.Name == candidate {
				return z.ID, nil
			}
		}
	}
	return "", fmt.Errorf("Cloudflare 账号下找不到 %s 所属的 zone（域名是否在这个账号里？）", fqdn)
}

func (c *Cloudflare) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		blob, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(blob)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("构造 Cloudflare 请求: %w", err)
	}
	// 凭据只在头里。放进 URL 会进代理日志、进服务商访问日志，
	// 也会在出错时被原样打进我们自己的日志。
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Cloudflare: %w", err)
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var env cfEnvelope
	if err := json.Unmarshal(blob, &env); err != nil {
		return nil, fmt.Errorf("Cloudflare 返回了非 JSON 响应（HTTP %d）", resp.StatusCode)
	}
	if !env.Success {
		e := &apiError{provider: "Cloudflare", status: resp.StatusCode, message: "未给出原因"}
		if len(env.Errors) > 0 {
			e.code = fmt.Sprint(env.Errors[0].Code)
			e.message = env.Errors[0].Message
		}
		return nil, e
	}
	return env.Result, nil
}
