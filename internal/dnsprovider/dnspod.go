package dnsprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DNSPodAPI 是 dnspod.cn 的 API 入口。
const DNSPodAPI = "https://dnsapi.cn"

// DNSPod 的接口有几个必须照原样对付的怪癖：
//
//  1. 全部走 POST + form 编码，不是 JSON
//  2. 凭据是 login_token=<ID>,<Token> 这一个字段
//  3. **HTTP 恒为 200**，成败看 status.code（"1" 才是成功）
//  4. 未设权重的记录 weight 是 JSON null，不是 0
//  5. 空记录列表返回 code 10，那不是错误
//
// 第 3 条最要命：只看 HTTP 状态码的话，凭据过期会被当成成功——
// 而现象是「保存成功但解析没变」，没人会想到去查凭据。
type DNSPodConfig struct {
	ID    string
	Token string
	// BaseURL 仅供测试指向本地服务端。
	BaseURL string
	HTTP    *http.Client
}

type DNSPod struct {
	cfg  DNSPodConfig
	base string
	http *http.Client
}

func NewDNSPod(cfg DNSPodConfig) *DNSPod {
	base := cfg.BaseURL
	if base == "" {
		base = DNSPodAPI
	}
	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &DNSPod{cfg: cfg, base: strings.TrimRight(base, "/"), http: hc}
}

func (d *DNSPod) Name() string          { return "DNSPod" }
func (d *DNSPod) SupportsLines() bool   { return true }
func (d *DNSPod) SupportsWeights() bool { return true }

// lineIDs 是线路名到 DNSPod record_line_id 的映射。
//
// 用 line_id 而不是中文线路名传参：线路名在不同套餐下的字面量会变，
// 而 ID 是稳定的。
var lineIDs = map[Line]string{
	LineDefault:  "0",
	LineTelecom:  "10=0",
	LineUnicom:   "10=1",
	LineMobile:   "10=3",
	LineTaiwan:   "80=2",
	LineOverseas: "80=0",
}

var lineByID = func() map[string]Line {
	m := map[string]Line{}
	for k, v := range lineIDs {
		m[v] = k
	}
	return m
}()

type dpStatus struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type dpRecord struct {
	ID     json.Number `json:"id"`
	Name   string      `json:"name"`
	Type   string      `json:"type"`
	Value  string      `json:"value"`
	Line   string      `json:"line"`
	LineID string      `json:"line_id"`
	TTL    json.Number `json:"ttl"`
	// Weight 未设置时是 JSON null，用指针接住——用 int 会在解析阶段报错，
	// 而那条记录本身完全正常
	Weight *json.Number `json:"weight"`
}

// SetTXT 写一条 TXT 记录。
func (d *DNSPod) SetTXT(ctx context.Context, fqdn, value string) error {
	sub, domain := splitDomain(fqdn)
	_, err := d.call(ctx, "/Record.Create", url.Values{
		"domain":         {domain},
		"sub_domain":     {sub},
		"record_type":    {"TXT"},
		"record_line_id": {lineIDs[LineDefault]},
		"value":          {value},
		// 挑战记录只活几十秒；长 TTL 会让上一次的值在缓存里挡住这一次的校验
		"ttl": {"60"},
	})
	return err
}

// RemoveTXT 按名字 + 值精确删除，不碰同名的其它挑战记录。
func (d *DNSPod) RemoveTXT(ctx context.Context, fqdn, value string) error {
	sub, domain := splitDomain(fqdn)
	raw, err := d.call(ctx, "/Record.List", url.Values{
		"domain": {domain}, "sub_domain": {sub}, "record_type": {"TXT"},
	})
	if err != nil {
		return err
	}
	recs, err := parseRecords(raw)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.Value != value {
			continue
		}
		if _, err := d.call(ctx, "/Record.Remove", url.Values{
			"domain": {domain}, "record_id": {rec.ID.String()},
		}); err != nil {
			return err
		}
	}
	return nil
}

// ListA 读回线上实际的 A 记录。
//
// 界面靠它显示「库里的权重」与「线上实际解析」的差异——改了权重却没生效时，
// 不看线上就无从察觉。
func (d *DNSPod) ListA(ctx context.Context, domain string) ([]ARecord, error) {
	raw, err := d.call(ctx, "/Record.List", url.Values{"domain": {domain}})
	if err != nil {
		return nil, err
	}
	recs, err := parseRecords(raw)
	if err != nil {
		return nil, err
	}
	out := make([]ARecord, 0, len(recs))
	for _, rec := range recs {
		// 只要 A 记录：把 MX、CNAME 也算进来的话，调度会把邮件服务器当成节点
		if !strings.EqualFold(rec.Type, "A") {
			continue
		}
		line, known := lineByID[rec.LineID]
		if !known {
			line = Line(rec.Line)
		}
		out = append(out, ARecord{
			ID: rec.ID.String(), Sub: rec.Name, Value: rec.Value,
			Line: line, Weight: numOr(rec.Weight, 0), TTL: intOr(rec.TTL, 600),
		})
	}
	return out, nil
}

// ApplyPlan 把调度计划落到线上：已有的改、缺的建、多出来的删。
//
// **不是**「全删再全建」：那会在中间留下一个解析为空的窗口，域名在那几秒里
// 谁都访问不了。
//
// 顺序也重要——先改后建再删。反过来的话，删在前面同样会露出空窗。
func (d *DNSPod) ApplyPlan(ctx context.Context, domain string, targets []Target) error {
	if len(targets) == 0 {
		// 空计划会把域名解析清空。这道线在客户端这一层也守一遍，因为调度那边
		// 的归一化逻辑一旦出错，后果全落在这里。
		return fmt.Errorf("拒绝下发空的解析计划：那会把 %s 解析清空，比继续解析到一台可能只是心跳抖动的机器糟得多", domain)
	}

	live, err := d.ListA(ctx, domain)
	if err != nil {
		return err
	}
	// 线上记录按「线路 + IP」索引：同一个 IP 可以在多条线路上各有一条记录
	type key struct {
		line Line
		ip   string
	}
	liveByKey := map[key]ARecord{}
	for _, r := range live {
		liveByKey[key{r.Line, r.Value}] = r
	}

	wanted := map[key]Target{}
	for _, t := range targets {
		wanted[key{t.Line, t.IP}] = t
	}

	// 1) 已有的：权重不对就改
	for k, t := range wanted {
		cur, exists := liveByKey[k]
		if !exists {
			continue
		}
		if cur.Weight == t.Weight {
			continue
		}
		if _, err := d.call(ctx, "/Record.Modify", url.Values{
			"domain": {domain}, "record_id": {cur.ID}, "sub_domain": {cur.Sub},
			"record_type": {"A"}, "record_line_id": {lineIDs[t.Line]},
			"value": {t.IP}, "weight": {strconv.Itoa(t.Weight)},
		}); err != nil {
			return fmt.Errorf("更新 %s 的 %s 线路记录 %s: %w", domain, t.Line, t.IP, err)
		}
	}

	// 2) 缺的：建
	for k, t := range wanted {
		if _, exists := liveByKey[k]; exists {
			continue
		}
		if _, err := d.call(ctx, "/Record.Create", url.Values{
			"domain": {domain}, "sub_domain": {"@"},
			"record_type": {"A"}, "record_line_id": {lineIDs[t.Line]},
			"value": {t.IP}, "weight": {strconv.Itoa(t.Weight)}, "ttl": {"600"},
		}); err != nil {
			return fmt.Errorf("新增 %s 的 %s 线路记录 %s: %w", domain, t.Line, t.IP, err)
		}
	}

	// 3) 多出来的：删。放最后，前两步已经保证线上至少有一条能用的记录
	for k, cur := range liveByKey {
		if _, keep := wanted[k]; keep {
			continue
		}
		if _, err := d.call(ctx, "/Record.Remove", url.Values{
			"domain": {domain}, "record_id": {cur.ID},
		}); err != nil {
			return fmt.Errorf("删除 %s 的过期记录 %s: %w", domain, cur.Value, err)
		}
	}
	return nil
}

func parseRecords(raw json.RawMessage) ([]dpRecord, error) {
	var body struct {
		Records []dpRecord `json:"records"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("解析 DNSPod 记录列表: %w", err)
	}
	return body.Records, nil
}

// call 发一次请求。HTTP 恒为 200，成败看 status.code。
func (d *DNSPod) call(ctx context.Context, path string, form url.Values) (json.RawMessage, error) {
	// 凭据只在表单体里，不进 URL：URL 会进代理日志、进服务商访问日志
	form.Set("login_token", d.cfg.ID+","+d.cfg.Token)
	form.Set("format", "json")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.base+path,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("构造 DNSPod 请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// DNSPod 要求带 UserAgent，缺了会被拒
	req.Header.Set("User-Agent", "edge-controller/1.0 (+https://github.com/xltxb/edge_caddy)")

	resp, err := d.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 DNSPod: %w", err)
	}
	defer resp.Body.Close()
	blob, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var head struct {
		Status dpStatus `json:"status"`
	}
	if err := json.Unmarshal(blob, &head); err != nil {
		return nil, fmt.Errorf("DNSPod 返回了非 JSON 响应（HTTP %d）", resp.StatusCode)
	}
	switch head.Status.Code {
	case "1":
		return blob, nil
	case "10":
		// 「没有记录」不是错误：一个还没配过解析的域名不该让整个调度页报错
		return json.RawMessage(`{"records":[]}`), nil
	default:
		return nil, &apiError{
			provider: "DNSPod", status: resp.StatusCode,
			code: head.Status.Code, message: head.Status.Message,
		}
	}
}

// splitDomain 把 FQDN 拆成 (子域, 主域)。
//
// DNSPod 收的是 sub_domain + domain，不是完整 FQDN。传错会在根域下建出一条
// `_acme-challenge.example.com.example.com`。
//
// 这里按「最后两段是主域」处理，对 example.com 这类常见形态正确；
// 对 co.uk 这类多级后缀不成立——那需要 Public Suffix List，
// 而本系统的域名都由用户在面板上录入，主域是已知的。
func splitDomain(fqdn string) (sub, domain string) {
	fqdn = strings.TrimSuffix(fqdn, ".")
	parts := strings.Split(fqdn, ".")
	if len(parts) <= 2 {
		return "@", fqdn
	}
	return strings.Join(parts[:len(parts)-2], "."), strings.Join(parts[len(parts)-2:], ".")
}

func numOr(n *json.Number, def int) int {
	if n == nil {
		return def
	}
	v, err := strconv.Atoi(n.String())
	if err != nil {
		return def
	}
	return v
}

func intOr(n json.Number, def int) int {
	v, err := strconv.Atoi(n.String())
	if err != nil {
		return def
	}
	return v
}
