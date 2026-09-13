package dnsctl

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

	"github.com/xltxb/edge_caddy/internal/dnssched"
)

// Cloudflare 用 Load Balancing 做加权调度。
//
// **纯 Cloudflare DNS 做不到这件事**：DNS 记录既没有权重字段也没有线路概念。
// Load Balancing 是独立付费产品，而且它的地理维度是国家/大洲——
// 电信/联通/移动这个分法在那边根本表达不了。
//
// 因此这个适配把 ct/cu/cm 塌缩成「中国」。三者权重不同时它**明确报错**：
// 取个平均值会给出一个用户没要过的配置，而且没人会发现。
// cfAPI 是两个 Cloudflare 适配共用的那一层：鉴权、发请求、翻错误码。
//
// **抽出来是因为它们必须一样。** 分开写的话，「权限不足该去改哪里」这类
// 翻译只会加在其中一个上，而人撞到的是另一个——那时候的症状是
// 「同一个错误，有的地方说得清有的地方说不清」，而它看起来像是随机的。
type cfAPI struct {
	// Token 是 API Token；GlobalKey + Email 是旧的 Global API Key 模式。
	Token     string
	Email     string
	GlobalKey string

	HTTP *http.Client
	Base string
}

// Cloudflare 用 Load Balancing 做加权调度。要账号级的 Load Balancing 权限，
// 而且那是个付费附加产品。用不了它的话看 CloudflareDNS。
type Cloudflare struct {
	cfAPI

	AccountID string
	ZoneID    string
	Hostname  string
}

func NewCloudflare(accountID, zoneID, hostname string) *Cloudflare {
	return &Cloudflare{
		cfAPI:     newCFAPI(),
		AccountID: accountID, ZoneID: zoneID, Hostname: hostname,
	}
}

func newCFAPI() cfAPI {
	return cfAPI{
		HTTP: &http.Client{Timeout: 20 * time.Second},
		Base: "https://api.cloudflare.com/client/v4",
	}
}

func (c *Cloudflare) Caps() Caps {
	return Caps{
		Kind: "cloudflare",
		// 中国这一组盖住 ct/cu/cm 三条线：Cloudflare 的地理维度是国家，
		// 分不出 ISP。Covers 让界面把这三条合并成一个输入框——
		// 于是「三条线权重不同」这个会被拒绝的状态在界面上造不出来。
		Lines: []LineCap{
			{Code: "cn", Name: "中国（电信 / 联通 / 移动合并）", Covers: []string{"ct", "cu", "cm"}},
			{Code: "tw", Name: "台湾", Covers: []string{"tw"}},
			{Code: "ov", Name: "境外", Covers: []string{"ov"}},
		},
		Weights: true,
		Notes: "Cloudflare 的 DNS 记录没有权重与线路概念，加权调度经 Load Balancing 实现" +
			"（独立付费产品）。它的地理维度是国家/大洲，电信 / 联通 / 移动无法区分，" +
			"三者会被合并为「中国」；三条线权重不同时下发会被拒绝。",
	}
}

// cnLines 是会被塌缩成「中国」的那几条。
var cnLines = []string{"ct", "cu", "cm"}

// Sync 把每个地理分组同步成一个 pool，再把 pool 挂到 load balancer 上。
func (c *Cloudflare) Sync(ctx context.Context, plan dnssched.Plan) error {
	groups, err := collapseForCloudflare(plan)
	if err != nil {
		return err
	}

	poolIDs := map[string]string{}
	for _, name := range sortedKeys(groups) {
		// 一个 origin 都没有的分组不建 pool：空 pool 既没有意义，
		// Cloudflare 也会拒绝（pool 至少要有一个 origin）。
		// 那条线路因此不会出现在 country_pools 里，流量落到 default。
		if len(groups[name]) == 0 {
			continue
		}
		id, err := c.syncPool(ctx, plan.Domain+"-"+name, groups[name])
		if err != nil {
			// **说清做到哪一步了。**
			//
			// 只回叶子错误的话，账号里此刻是「cn 的 pool 是新的、load balancer
			// 还指着旧 pool」，而同步状态只说「失败」——人不知道该去收拾什么
			// （issue #57）。「什么都没做就失败」和「做了一半」要人做的事
			// 完全不同。
			//
			// 这个包别处已经是这个标准了：dnsops.Sync 的「N 个域名里 M 个成功」。
			return fmt.Errorf("%s：已更新 %s，load balancer 未改动（仍指向旧 pool）：%w",
				name, doneSoFar(poolIDs), err)
		}
		poolIDs[name] = id
	}
	if len(poolIDs) == 0 {
		// 一个节点都不在轮换里。这里**不去清空 load balancer**：
		// 把最后一条记录撤掉等于主动让域名解析不出来，而这多半是一次
		// 短暂的全体离线。宁可让流量继续打到已知的机器上，也不要主动制造
		// 一次 NXDOMAIN。
		return capErr("没有任何节点在解析轮换里，本次不改动 Cloudflare 配置")
	}
	return c.syncLoadBalancer(ctx, poolIDs)
}

// collapseForCloudflare 把五条线塌缩成 cn / tw / ov 三组。
//
// ct/cu/cm 三者的安排必须一致，否则这份配置在 Cloudflare 上表达不出来。
// 报错而不是取平均：一个用户没要过的配置比一个明确的拒绝糟糕得多，
// 尤其它还不会被发现。
func collapseForCloudflare(plan dnssched.Plan) (map[string][]dnssched.Entry, error) {
	byLine := map[string][]dnssched.Entry{}
	for _, lp := range plan.Lines {
		byLine[lp.Code] = plan.Rotation(lp.Code)
	}

	ref := signature(byLine[cnLines[0]])
	for _, l := range cnLines[1:] {
		if signature(byLine[l]) != ref {
			return nil, capErr(
				"Cloudflare 无法区分电信 / 联通 / 移动（它的地理维度是国家，不是 ISP 线路）。"+
					"这三条线必须配置相同的节点与权重，当前 %s 与 %s 不一致。",
				lineName(cnLines[0]), lineName(l))
		}
	}

	return map[string][]dnssched.Entry{
		"cn": byLine["ct"],
		"tw": byLine["tw"],
		"ov": byLine["ov"],
	}, nil
}

func signature(entries []dnssched.Entry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, fmt.Sprintf("%s=%d", e.Node, e.Weight))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func lineName(code string) string {
	for _, l := range dnssched.Lines {
		if l.Code == code {
			return l.Name
		}
	}
	return code
}

type cfOrigin struct {
	Name    string  `json:"name"`
	Address string  `json:"address"`
	Enabled bool    `json:"enabled"`
	Weight  float64 `json:"weight"`
}

type cfPool struct {
	ID      string     `json:"id,omitempty"`
	Name    string     `json:"name"`
	Enabled bool       `json:"enabled"`
	Origins []cfOrigin `json:"origins"`
}

// syncPool 建或改一个 pool，返回它的 id。
func (c *Cloudflare) syncPool(ctx context.Context, name string, entries []dnssched.Entry) (string, error) {
	pools, err := c.listPools(ctx)
	if err != nil {
		return "", err
	}

	// Cloudflare 的 origin weight 是 0–1 的小数，不是我们那个整数权重。
	// 归一化在这里做——总和为 0 时全部给 0，不做除法。
	var total int
	for _, e := range entries {
		total += e.Weight
	}
	origins := make([]cfOrigin, 0, len(entries))
	for _, e := range entries {
		w := 0.0
		if total > 0 {
			w = float64(e.Weight) / float64(total)
		}
		origins = append(origins, cfOrigin{
			Name: e.Node, Address: e.IP, Enabled: true, Weight: w,
		})
	}

	body := cfPool{Name: name, Enabled: len(origins) > 0, Origins: origins}
	if id, ok := pools[name]; ok {
		var out struct{ Result cfPool }
		if err := c.call(ctx, http.MethodPut,
			"/accounts/"+c.AccountID+"/load_balancers/pools/"+id, body, &out); err != nil {
			return "", err
		}
		return id, nil
	}
	var out struct{ Result cfPool }
	if err := c.call(ctx, http.MethodPost,
		"/accounts/"+c.AccountID+"/load_balancers/pools", body, &out); err != nil {
		return "", err
	}
	return out.Result.ID, nil
}

func (c *Cloudflare) listPools(ctx context.Context) (map[string]string, error) {
	var out struct{ Result []cfPool }
	if err := c.call(ctx, http.MethodGet,
		"/accounts/"+c.AccountID+"/load_balancers/pools", nil, &out); err != nil {
		return nil, err
	}
	byName := map[string]string{}
	for _, p := range out.Result {
		byName[p.Name] = p.ID
	}
	return byName, nil
}

type cfLoadBalancer struct {
	ID             string              `json:"id,omitempty"`
	Name           string              `json:"name"`
	DefaultPools   []string            `json:"default_pools"`
	FallbackPool   string              `json:"fallback_pool,omitempty"`
	CountryPools   map[string][]string `json:"country_pools,omitempty"`
	SteeringPolicy string              `json:"steering_policy"`
	Proxied        bool                `json:"proxied"`
}

func (c *Cloudflare) syncLoadBalancer(ctx context.Context, poolIDs map[string]string) error {
	var list struct{ Result []cfLoadBalancer }
	if err := c.call(ctx, http.MethodGet,
		"/zones/"+c.ZoneID+"/load_balancers", nil, &list); err != nil {
		return err
	}

	lb := cfLoadBalancer{
		Name:           c.Hostname,
		SteeringPolicy: "geo",
		DefaultPools:   nonEmpty(poolIDs["ov"], poolIDs["cn"]),
		FallbackPool:   firstNonEmpty(poolIDs["ov"], poolIDs["cn"]),
		CountryPools:   map[string][]string{},
	}
	if id := poolIDs["cn"]; id != "" {
		lb.CountryPools["CN"] = []string{id}
	}
	if id := poolIDs["tw"]; id != "" {
		lb.CountryPools["TW"] = []string{id}
	}

	for _, cur := range list.Result {
		if cur.Name == c.Hostname {
			return c.call(ctx, http.MethodPut,
				"/zones/"+c.ZoneID+"/load_balancers/"+cur.ID, lb, nil)
		}
	}
	return c.call(ctx, http.MethodPost, "/zones/"+c.ZoneID+"/load_balancers", lb, nil)
}

func nonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *cfAPI) call(ctx context.Context, method, path string, body, out any) error {
	// **拼出空路径段就地拦下，别把它发出去。**
	//
	// account_id 为空时 "/accounts/"+id+"/load_balancers/pools" 会变成
	// `/accounts//load_balancers/pools`，而 Cloudflare 回的是
	// 7003「Could not route to ...，perhaps your object identifier is invalid?」
	// —— 一句让人去查 token 和权限的话，而真正的原因是一个字段没填。
	//
	// 校验那一侧（store.MissingFields）现在会拦住这种配置，所以正常情况下
	// 走不到这里。留着它是因为**下一个拼进 URL 的字段不会来问我**：
	// 判据在别的包里，而这一行就在拼接的旁边。
	if strings.Contains(path, "//") {
		return fmt.Errorf("请求路径里有空的一段（%s）—— "+
			"account_id 或 zone_id 没填，这个请求发出去只会换回一句「路由不到」", path)
	}

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// 两种凭证模式（PRD §5 明确要区分）：API Token 是 Bearer，
	// 旧的 Global API Key 走 X-Auth-Email + X-Auth-Key。
	switch {
	case c.Token != "":
		req.Header.Set("Authorization", "Bearer "+c.Token)
	case c.GlobalKey != "" && c.Email != "":
		req.Header.Set("X-Auth-Email", c.Email)
		req.Header.Set("X-Auth-Key", c.GlobalKey)
	default:
		return fmt.Errorf("Cloudflare 凭证未配置")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("调用 Cloudflare: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	var env cfEnvelope
	// **丢弃这个错误是安全的，理由在下一行。**
	//
	// 解析失败时 env.Success 保持零值 false，紧接着的 !env.Success 就会
	// 进错误分支，并把原始响应体原样带出去（Cloudflare 返回 HTML 错误页时
	// 正是这条路）。丢弃它不会变成一次静默的成功。
	//
	// 写下这条是因为这个关系是**隐式**的：读的人得自己往下看一行才能推出来，
	// 而「丢弃错误」这个动作本身长得跟真正的疏忽一模一样。
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !env.Success {
		msg := strings.TrimSpace(string(raw))
		if len(env.Errors) > 0 {
			// 原样带上服务商的措辞：那是排查凭证权限不足的唯一线索，
			// 而权限不足是接 Cloudflare 时最常见的失败。
			msg = fmt.Sprintf("%d %s", env.Errors[0].Code, env.Errors[0].Message)
			if h := hint(env.Errors[0].Code, path); h != "" {
				msg += "。" + h
			}
		}
		return fmt.Errorf("Cloudflare %s %s: %s", method, path, msg)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// hint 把 Cloudflare 的错误码翻成一句**说得出该去改哪里**的话。
//
// 上面那段注释写着「权限不足是接 Cloudflare 时最常见的失败」，而在
// 这个函数存在之前，消息里没有一个字说该去改什么 ——
// **描述清楚一个危害，不等于处置了它。**
//
// 原始措辞照旧原样带上（它是唯一的一手证据），这只是在它后面补一句。
// 认不出的码返回空串：**编一句像模像样的猜测比不说更坏**，
// 它会把人送去一个由我们凭空指定的方向。
func hint(code int, path string) string {
	switch code {
	case 10000:
		// Authentication error：凭证本身无效，或者它对**这个资源**没权限。
		// 加权调度要两处，而它们在 Token 编辑页是两个不同的区块 ——
		// 只给 DNS 权限的 Token 是最常见的那一种，而它在这里失败。
		if strings.Contains(path, "/load_balancers") {
			return "这个端点要 Load Balancing 权限，只有 DNS 权限的 Token 到不了这里：" +
				"账户级「Load Balancing: Monitors and Pools」= Edit，" +
				"区域级「Load Balancers」= Edit，两个都要。" +
				"另外 Load Balancing 是 Cloudflare 的付费附加产品，账户没开通时权限给全了也用不了"
		}
		return "凭证无效，或者它对这个资源没有权限"
	case 7003:
		// 正常情况下 call() 开头那道拦截先挡住了，走到这里说明是别的空段。
		return "请求路径里有空的一段 —— 多半是某个 ID 没填"
	}
	return ""
}

// doneSoFar 把已经建好的那些分组名排出来，供中途失败时说清进度。
func doneSoFar(poolIDs map[string]string) string {
	if len(poolIDs) == 0 {
		return "（尚未更新任何分组）"
	}
	return strings.Join(sortedKeys(poolIDs), " / ")
}
