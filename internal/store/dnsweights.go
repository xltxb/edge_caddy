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

	// ClearCredential 是删除凭证的信号，**不落库**。
	//
	// 需要一个独立字段是因为空串已经被占用了：它表示「不改动」
	// （凭证不回显，前端带不出原值）。用空串表示删除的话，
	// 一次「只改域名」的保存会顺手把凭证清掉。
	ClearCredential bool `json:"-"`
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
	if c.Domain == "" {
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
		if c.ZoneID == "" {
			missing = append(missing, "zone_id") // load balancer 挂在 zone 上
		}
		// Global API Key 模式要 email 配对；API Token 模式不要。
		if c.CredentialMode == "global_key" && c.Email == "" {
			missing = append(missing, "email")
		}
	}
	return missing
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
