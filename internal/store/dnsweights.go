package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xltxb/edge_caddy/internal/secret"
)

const KeyDNS = "dns"

// DNSProviderSettings 是服务商接入配置。凭证加密落库、任何接口不回显（PRD §7）。
type DNSProviderSettings struct {
	Kind      string `json:"kind"`   // dnspod | cloudflare | ""（未配置）
	Domain    string `json:"domain"` // 根域名
	SubName   string `json:"sub"`    // 子域名，@ 表示根
	AccountID string `json:"account_id,omitempty"`
	ZoneID    string `json:"zone_id,omitempty"`
	Email     string `json:"email,omitempty"`

	// CredentialMode 是 Cloudflare 的两种凭证形态之一：api_token | global_key。
	CredentialMode string `json:"credential_mode,omitempty"`

	// 明文只在装配服务商客户端时出现，不经任何读接口回显。
	Credential   string `json:"-"`
	CredentialOK bool   `json:"-"`

	// Targets 是这套系统要管的**全部**主机名。
	//
	// **一份配置管多个域名**：客户的域名各自要有 A 记录（根域名 CNAME 不了），
	// 而它们可能分属不同的 zone。凭证与 kind 只有一份（同一个服务商账号），
	// 变的是每条记录写到哪儿。
	//
	// 轮换是**共享**的：所有域名指向同一组边缘节点，权重表不分域名。
	// 那是 CDN 的常规形态——分域名配不同节点是另一件事，没做。
	//
	// **Domain / SubName / ZoneID 那三个字段是旧形态**，只剩兼容用途：
	// 读出来时若 Targets 为空而 Domain 非空，合成一条。写入时以 Targets 为准。
	// 不直接删掉它们是因为库里已经有旧数据，而一次读不出来的配置
	// 会表现成「解析突然不同步了」，且没有任何一处说得出为什么。
	Targets []DNSTarget `json:"targets,omitempty"`

	// ClearCredential 是删除凭证的信号，**不落库**。
	//
	// 需要一个独立字段是因为空串已经被占用了：它表示「不改动」
	// （凭证不回显，前端带不出原值）。用空串表示删除的话，
	// 一次「只改域名」的保存会顺手把凭证清掉。
	ClearCredential bool `json:"-"`
}

// DNSTarget 是一个要管的主机名。
//
// ZoneID 只有 Cloudflare 用：**同一个 zone 下的多个子域填同一个 zone_id**，
// 不同顶级域各填各的。DNSPod 不需要它（它按 domain 自己找）。
type DNSTarget struct {
	Domain string `json:"domain"`
	Sub    string `json:"sub,omitempty"`
	ZoneID string `json:"zone_id,omitempty"`
}

// Hostname 是这条目标实际写入的名字。
func (t DNSTarget) Hostname() string {
	if t.Domain == "" {
		return ""
	}
	if t.Sub == "" || t.Sub == "@" {
		return t.Domain
	}
	return t.Sub + "." + t.Domain
}

// EffectiveTargets 是「这份配置实际要管哪些主机名」。
//
// **旧形态在这里被合成一条**，而不是在读库那一层改写：改写会把旧数据
// 静默升级，于是「它原本是什么样」这件事永远消失了——而排查一次
// 「解析写到了奇怪的地方」时，那正是要问的第一个问题。
func (c DNSProviderSettings) EffectiveTargets() []DNSTarget {
	if len(c.Targets) > 0 {
		return c.Targets
	}
	if c.Domain == "" {
		return nil
	}
	return []DNSTarget{{Domain: c.Domain, Sub: c.SubName, ZoneID: c.ZoneID}}
}

// MissingFields 列出这份服务商配置还差什么才**能用**。
//
// **它是校验与装配共用的那一个判据，这一点是承重的。**
//
// 灰度上撞到的：`PUT /settings` 只校验了 kind，而装配服务商要求
// kind + domain + credential 三样都有。于是一份只有 kind 和凭证的配置
// 被收下了，设置页显示「已配置」，而 DNS 页说「尚未配置服务商」——
// **两个端点对同一件事说了相反的话，而两句在各自的口径下都对**。
//
// 人看到的是：填完保存成功、徽标变绿、而解析一动不动，
// 没有任何一处说得出缺了什么。
//
// 分成两处写迟早会分叉：加一个新的必填字段时，改了装配那一侧、
// 忘了校验那一侧，症状就是这次这个——**而它不报错**。
func (c DNSProviderSettings) MissingFields() []string {
	var missing []string
	if c.Kind == "" {
		missing = append(missing, "kind")
	}
	if len(c.EffectiveTargets()) == 0 {
		missing = append(missing, "domain")
	}
	// 凭证不回显，所以判据是「库里有没有」而不是「这次请求带没带」。
	if c.Credential == "" && !c.CredentialOK {
		missing = append(missing, "credential")
	}

	// **各家要的东西不一样，而这个判据必须知道这件事。**
	//
	// 灰度上撞到的：Cloudflare 只填了 kind / domain / token 就保存成功了，
	// 而推权重时报 `GET /accounts//load_balancers/pools`——**那个双斜杠**
	// 就是 account_id 为空。Cloudflare 报的是 7003「路由不到」，
	// 一句和「你少填了一项」毫无关系的话。
	//
	// 上面三项是「哪家都要」，它们守不住这个：**一份配置可以三项齐全
	// 而对这一家仍然不能用**。判据缺了服务商这一维，于是它对 Cloudflare
	// 的回答一直是「够了」。
	//
	// 契约那张表把 account_id 写成「可选」，前端的输入框因此标着「可空」
	// ——**是我写错了，而前端是照着做的**。表已改。
	switch c.Kind {
	case "cloudflare":
		// 两个都是拼进 URL 的路径段：空了不会报「缺参数」，
		// 会拼出 //，然后由对方回一句风马牛不相及的错。
		if c.AccountID == "" {
			missing = append(missing, "account_id") // 加权调度用的 pool 是账号级的
		}
		// **每个目标各要一个 zone_id**：load balancer 挂在 zone 上，
		// 而不同顶级域是不同的 zone。少任何一个，那个域名就静默地不被管。
		if c.targetMissingZone() {
			missing = append(missing, "zone_id")
		}
		// Global API Key 模式要 email 配对；API Token 模式不要。
		if c.CredentialMode == "global_key" && c.Email == "" {
			missing = append(missing, "email")
		}
	case "cloudflare_dns":
		// **不要 account_id。** 普通 DNS 记录挂在 zone 上，
		// 账号级的 Load Balancing 权限根本用不上——这正是这条路的好处之一：
		// 少一个要人去 Cloudflare 后台翻的值，也少一类权限不足。
		if c.targetMissingZone() {
			missing = append(missing, "zone_id")
		}
		if c.CredentialMode == "global_key" && c.Email == "" {
			missing = append(missing, "email")
		}
	}
	return missing
}

// ProviderKinds 是主控认得的服务商。**这是唯一的一份名单。**
//
// 此前 settings 的校验里写着 `k != "dnspod" && k != "cloudflare"`，
// 而 dnsops 的装配是另一个 switch —— 加一家时改了一处忘了另一处，
// 症状是「保存成功、装配时报未知服务商」，或者反过来「代码支持它、而校验不让存」。
var ProviderKinds = []string{"dnspod", "cloudflare", "cloudflare_dns"}

// KnownKind 说这个 kind 主控认不认。
func KnownKind(k string) bool {
	for _, x := range ProviderKinds {
		if x == k {
			return true
		}
	}
	return false
}

// CredentialModes 是每家支持的凭证模式。空串表示这一家没有模式之分。
var CredentialModes = map[string][]string{
	"dnspod":         {""},
	"cloudflare":     {"api_token", "global_key"},
	"cloudflare_dns": {"api_token", "global_key"},
}

// ProviderRequirements 报出每个 kind × mode 还要人填哪些字段。
//
// **它是 MissingFields 对一份空配置求值的结果，不是它的抄本。**
// 这一点是承重的：前端要拿它去检查「界面上有没有这个输入框」，
// 而一份会和判据分叉的清单，守出来的一致性是假的。
//
// 起因：Cloudflare 的 account_id 此前被误关在 global_key 分支里，
// **api_token 模式下界面上根本不渲染那个框**。人不是留空了它，是填不了它——
// 而一个不存在的框不引起任何疑问，一个标着「可空」的空框至少还在页面上。
//
// **不含 kind**：那是选择器本身，不是要填的字段。
// 键名与 `PUT /settings` 的 `dns_provider` body 键**一模一样**，
// 前端拿它当找输入框的钥匙——两边叫法不同的话就得再加一层映射，
// 而那层映射又是一份新知识。
func ProviderRequirements() map[string]map[string][]string {
	out := map[string]map[string][]string{}
	for _, kind := range ProviderKinds {
		byMode := map[string][]string{}
		for _, mode := range CredentialModes[kind] {
			byMode[mode] = DNSProviderSettings{Kind: kind, CredentialMode: mode}.MissingFields()
		}
		out[kind] = byMode
	}
	return out
}

// targetMissingZone 说有没有哪个目标缺 zone_id。
//
// **判据是「有没有一个缺」，不是「第一个缺不缺」。** 缺一个的后果是那一个
// 域名静默地不被管：其余的照常同步，界面上一片正常，而那个域名的记录
// 永远不会出现——**而人是按「解析同步成功了」去看的**。
func (c DNSProviderSettings) targetMissingZone() bool {
	targets := c.EffectiveTargets()
	// **一个目标都没有时也算缺。**
	//
	// 空切片遍历下来会返回 false —— 「没有任何一个缺」，
	// 而那读起来像「都齐了」。第一版就是这么写的，两条测试当场红：
	// ProviderRequirements 拿一份空配置求值，于是 zone_id 从必填清单里消失了，
	// 而前端那条「每个必填字段都得有输入框」的检查会跟着放行。
	//
	// **「一个都没有」和「都齐了」在遍历里长得一模一样**，
	// 而这一整天数的就是这个形状。
	if len(targets) == 0 {
		return true
	}
	for _, t := range targets {
		if t.ZoneID == "" {
			return true
		}
	}
	return false
}

// Usable 说这份配置装配得出一个能用的服务商客户端。
func (c DNSProviderSettings) Usable() bool { return len(c.MissingFields()) == 0 }

type dnsRow struct {
	DNSProviderSettings
	CredB64 string `json:"credential_sealed,omitempty"`
}

func (s *Store) GetDNSProvider(ctx context.Context, sealer *secret.Sealer) (DNSProviderSettings, error) {
	var row dnsRow
	raw, err := s.rawSettings(ctx, KeyDNS)
	if err != nil || raw == nil {
		return DNSProviderSettings{}, err
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return DNSProviderSettings{}, fmt.Errorf("解析 DNS 服务商设置: %w", err)
	}
	out := row.DNSProviderSettings
	out.CredentialOK = row.CredB64 != ""
	if sealer != nil && row.CredB64 != "" {
		if out.Credential, err = openB64(sealer, row.CredB64); err != nil {
			return out, fmt.Errorf("解开 DNS 凭证: %w", err)
		}
	}
	return out, nil
}

// PutDNSProvider 写入。凭证为空串表示保持不变——它不回显，前端也带不出原值。
func (s *Store) PutDNSProvider(ctx context.Context, in DNSProviderSettings, sealer *secret.Sealer) error {
	var row dnsRow
	if raw, err := s.rawSettings(ctx, KeyDNS); err != nil {
		return err
	} else if raw != nil {
		if err := json.Unmarshal(raw, &row); err != nil {
			return fmt.Errorf("解析已有 DNS 设置: %w", err)
		}
	}
	row.DNSProviderSettings = in
	if in.ClearCredential {
		// **凭证的唯一一条删除路径。**
		//
		// 空串对凭证是「不改动」（凭证不回显，前端带不出原值），
		// 所以清掉它需要一个跟「留空」分得开的信号。没有这条路径的话，
		// 库里会留下一份**再也用不到、也删不掉**的凭证——而它仍然是一把
		// 有效的 API Token。
		row.CredB64 = ""
		row.Credential = ""
		return s.putSettings(ctx, KeyDNS, row)
	}
	if in.Credential != "" {
		if sealer == nil {
			return fmt.Errorf("要写入 DNS 凭证，但没有可用的密封器（装配漏了 Sealer）")
		}
		b, err := sealer.Seal([]byte(in.Credential))
		if err != nil {
			return err
		}
		row.CredB64 = base64.StdEncoding.EncodeToString(b)
	}
	return s.putSettings(ctx, KeyDNS, row)
}

// DNSWeights 是 line_code → node_id → weight。
//
// 用裸 map 而不是引 dnssched.Weights：规划层必须保持纯算术、不碰存储，
// 而仓储层反过来依赖它就会成环。两者底层类型相同，转换是免费的。
type DNSWeights map[string]map[string]int

// GetDNSWeights 读出全部线路的权重配置。
func (s *Store) GetDNSWeights(ctx context.Context) (DNSWeights, error) {
	rows, err := s.Pool.Query(ctx, `SELECT line_code, node_id, weight FROM dns_weights`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := DNSWeights{}
	for rows.Next() {
		var line, node string
		var w int
		if err := rows.Scan(&line, &node, &w); err != nil {
			return nil, err
		}
		if out[line] == nil {
			out[line] = map[string]int{}
		}
		out[line][node] = w
	}
	return out, rows.Err()
}

// PutDNSWeights 整体替换权重配置。
//
// 整体替换而不是逐条 upsert：前端提交的是一整份安排，
// 逐条更新会让「删掉某个节点的权重」这件事没有表达方式，
// 于是那一行会静静留在库里继续参与归一化。
func (s *Store) PutDNSWeights(ctx context.Context, w DNSWeights) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM dns_weights`); err != nil {
		return err
	}
	for line, nodes := range w {
		for node, weight := range nodes {
			if _, err := tx.Exec(ctx,
				`INSERT INTO dns_weights (line_code, node_id, weight) VALUES ($1,$2,$3)`,
				line, node, weight); err != nil {
				return fmt.Errorf("写入 %s/%s 的权重: %w", line, node, err)
			}
		}
	}
	return tx.Commit(ctx)
}

const KeyDNSSync = "dns_sync"

// DNSSyncState 是**最近一次**把解析安排推给服务商的结果。
//
// 它必须落库，因为界面上那个「已退出解析」徽标是**常驻**的，而一次请求响应里的
// dns_synced 会消失。一次失败的同步之后，常驻徽标会一直说「这台机器不接流量了」
// ——而它照旧在解析里，直到下次有人再点一次开关才被纠正。
//
// **常驻的说法需要常驻的真相来源。**
type DNSSyncState struct {
	OK bool `json:"ok"`
	// At 为 null 表示**从来没同步过**，不是零值时间（契约 §0.4）。
	//
	// Go 的零值时间序列化成 0001-01-01T00:00:00Z，界面上会渲染成 00:00:00
	// ——读起来像「凌晨同步过一次」。**一个格式正确但意思是假的值，比缺失的值
	// 危险**：空白会让人去查，一个像模像样的时间不会。
	At     *time.Time `json:"at"`
	Detail string     `json:"detail"`

	// Targets 是**每个域名各自**的结果。
	//
	// 一个布尔说不出「三个域名里哪一个没同步上」，而人会按那个布尔决定
	// 要不要去查。三个里坏一个时，OK 是 false、Detail 说第一条错——
	// **而另外两个是好的这件事，只有这一列说得出来**。
	//
	// 旧数据里没有它，那时它是 nil：**不是「一个目标都没有」**，
	// 是「这条记录写于只支持单域名的版本」。界面据此退回只显示 Detail。
	Targets []DNSTargetSync `json:"targets,omitempty"`
}

// DNSTargetSync 是一个域名这一次的同步结果。
type DNSTargetSync struct {
	Hostname string `json:"hostname"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail"`
}

func (s *Store) GetDNSSync(ctx context.Context) (DNSSyncState, error) {
	raw, err := s.rawSettings(ctx, KeyDNSSync)
	if err != nil || raw == nil {
		// 从来没同步过。ok=false 是对的：服务商那边确实没反映过我们的意图。
		return DNSSyncState{Detail: "尚未向 DNS 服务商同步过"}, err
	}
	var out DNSSyncState
	return out, json.Unmarshal(raw, &out)
}

func (s *Store) PutDNSSync(ctx context.Context, st DNSSyncState) error {
	return s.putSettings(ctx, KeyDNSSync, st)
}
