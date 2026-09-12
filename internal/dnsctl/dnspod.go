package dnsctl

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

	"github.com/xltxb/edge_caddy/internal/dnssched"
)

// dnspodLines 把内部线路码映射到 DNSPod 的线路名。
//
// **未经真账号核对。** DNSPod 的线路可用性与账号套餐有关，接入时应当先用
// Record.List 之类的只读接口确认这些名字在目标域名上是合法的。
var dnspodLines = map[string]string{
	"ct": "电信",
	"cu": "联通",
	"cm": "移动",
	"tw": "中国台湾",
	"ov": "境外",
}

// DNSPod 用 dnsapi.cn 的 Record.* 接口。
type DNSPod struct {
	// Token 形如 "<ID>,<Token>"，即 DNSPod 的 login_token。
	Token   string
	Domain  string
	SubName string // 子域名，@ 表示根
	TTL     int
	HTTP    *http.Client
	Base    string // 便于测试指向模拟服务端
}

func NewDNSPod(token, domain, sub string) *DNSPod {
	if sub == "" {
		sub = "@"
	}
	return &DNSPod{
		Token: token, Domain: domain, SubName: sub, TTL: 60,
		HTTP: &http.Client{Timeout: 15 * time.Second},
		Base: "https://dnsapi.cn",
	}
}

func (d *DNSPod) Caps() Caps {
	return Caps{
		Kind: "dnspod",
		Lines: []LineCap{
			{Code: "ct", Name: "电信", Covers: []string{"ct"}},
			{Code: "cu", Name: "联通", Covers: []string{"cu"}},
			{Code: "cm", Name: "移动", Covers: []string{"cm"}},
			{Code: "tw", Name: "台湾", Covers: []string{"tw"}},
			{Code: "ov", Name: "境外", Covers: []string{"ov"}},
		},
		Weights: true,
		Notes:   "线路与权重均由 DNSPod 原生支持（权重需要付费套餐）。",
	}
}

type dnspodRecord struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Line   string `json:"line"`
	Type   string `json:"type"`
	Value  string `json:"value"`
	Weight *int   `json:"weight"`
}

// Sync 幂等地把每条线路的记录调成 Plan 描述的样子。
//
// 先列后改而不是先删后建：删了再建会有一个「这个域名没有任何 A 记录」的窗口，
// 而 DNS 解析恰恰在那一刻会失败。
func (d *DNSPod) Sync(ctx context.Context, plan dnssched.Plan) error {
	type key struct{ line, value string }

	want := map[key]int{} // → weight
	for _, lp := range plan.Lines {
		lineName, ok := dnspodLines[lp.Code]
		if !ok {
			continue
		}
		for _, e := range lp.Entries {
			if !e.InRotation || e.IP == "" {
				continue
			}
			want[key{lineName, e.IP}] = e.Weight
		}
	}

	// **不清空记录。** 把最后一条记录撤掉等于主动让域名解析不出来，
	// 而「一个节点都不在轮换里」多半是一次短暂的全体离线。
	// 宁可让流量继续打到已知的机器上，也不要主动制造一次 NXDOMAIN。
	// 与两个 Cloudflare 适配同一条规矩。
	//
	// **闸在 list 之前**：这一趟既然不打算改任何东西，连列都不必列。
	if len(want) == 0 {
		return capErr("没有任何节点在解析轮换里，本次不改动 DNS 记录")
	}

	existing, err := d.list(ctx)
	if err != nil {
		return err
	}

	// 按 线路名+IP 索引已有记录。
	have := map[key]dnspodRecord{}
	for _, r := range existing {
		if r.Type == "A" {
			have[key{r.Line, r.Value}] = r
		}
	}

	for k, weight := range want {
		if cur, ok := have[k]; ok {
			// **服务商表达不了权重时，记录在不在就是全部**。
			//
			// Weight 是 *int，因为权重是 DNSPod 的付费套餐特性——未开通的域名
			// 上它恒为 nil。原先写的是 `cur.Weight != nil && *cur.Weight ==
			// weight`，nil 时恒假，于是每一次自愈、每一次点开关都对全部记录
			// 发一轮 Modify（issue #54）。Provider 接口要求 Sync 幂等，
			// 而自愈会在节点抖动时反复调它。
			//
			// 症状离原因很远：撞上接口频率限制之后，人看到的是「摘除偶尔失败」。
			if cur.Weight == nil || *cur.Weight == weight {
				continue
			}
			if err := d.modify(ctx, cur.ID, k.line, k.value, weight); err != nil {
				return err
			}
			continue
		}
		if err := d.create(ctx, k.line, k.value, weight); err != nil {
			return err
		}
	}

	// 后删：到这一步该在的记录都已经在了，删掉多余的不会留下空窗。
	for k, r := range have {
		if _, keep := want[k]; keep {
			continue
		}
		if !isManagedLine(k.line) {
			// 只动我们认识的那几条线路。别人手工加的记录不该被这套东西清掉。
			continue
		}
		if err := d.remove(ctx, r.ID); err != nil {
			return err
		}
	}
	return nil
}

func isManagedLine(name string) bool {
	for _, v := range dnspodLines {
		if v == name {
			return true
		}
	}
	return false
}

func (d *DNSPod) list(ctx context.Context) ([]dnspodRecord, error) {
	var out struct {
		Status  dnspodStatus   `json:"status"`
		Records []dnspodRecord `json:"records"`
	}
	err := d.call(ctx, "/Record.List", url.Values{
		"domain":     {d.Domain},
		"sub_domain": {d.SubName},
	}, &out)
	if err != nil {
		return nil, err
	}
	// 「没有记录」在 DNSPod 里是错误码 10，不是空列表。
	if out.Status.Code == "10" {
		return nil, nil
	}
	if out.Status.Code != "1" {
		return nil, fmt.Errorf("DNSPod Record.List: %s", out.Status.Message)
	}
	return out.Records, nil
}

func (d *DNSPod) create(ctx context.Context, line, value string, weight int) error {
	return d.mutate(ctx, "/Record.Create", url.Values{
		"domain":      {d.Domain},
		"sub_domain":  {d.SubName},
		"record_type": {"A"},
		"record_line": {line},
		"value":       {value},
		"weight":      {strconv.Itoa(weight)},
		"ttl":         {strconv.Itoa(d.TTL)},
	})
}

func (d *DNSPod) modify(ctx context.Context, id, line, value string, weight int) error {
	return d.mutate(ctx, "/Record.Modify", url.Values{
		"domain":      {d.Domain},
		"record_id":   {id},
		"sub_domain":  {d.SubName},
		"record_type": {"A"},
		"record_line": {line},
		"value":       {value},
		"weight":      {strconv.Itoa(weight)},
		"ttl":         {strconv.Itoa(d.TTL)},
	})
}

func (d *DNSPod) remove(ctx context.Context, id string) error {
	return d.mutate(ctx, "/Record.Remove", url.Values{
		"domain":    {d.Domain},
		"record_id": {id},
	})
}

type dnspodStatus struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (d *DNSPod) mutate(ctx context.Context, path string, form url.Values) error {
	var out struct {
		Status dnspodStatus `json:"status"`
	}
	if err := d.call(ctx, path, form, &out); err != nil {
		return err
	}
	if out.Status.Code != "1" {
		// 原样带上服务商的措辞：那是排查凭证/套餐/线路不可用的唯一线索。
		return fmt.Errorf("DNSPod %s: %s", strings.TrimPrefix(path, "/"), out.Status.Message)
	}
	return nil
}

func (d *DNSPod) call(ctx context.Context, path string, form url.Values, out any) error {
	form.Set("login_token", d.Token)
	form.Set("format", "json")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Base+path,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// DNSPod 要求带 User-Agent，缺了会被拒。
	req.Header.Set("User-Agent", "edge-controller/0.1 (ops@internal)")

	resp, err := d.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("调用 DNSPod: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("DNSPod HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}
