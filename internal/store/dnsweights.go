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
}

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
