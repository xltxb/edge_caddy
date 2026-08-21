package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xltxb/edge_caddy/internal/secret"
)

// Cert 是主控**账面上**的一张证书。
//
// 证书建表，不是「从节点上报的清单聚合」（后端文档 §3 那条已被推翻）：
// 主控是签发方，必须持有 PEM 才能内联下发（ADR-0010）。
type Cert struct {
	Domain    string    `json:"domain"`
	Issuer    string    `json:"issuer"`
	Challenge string    `json:"challenge"`
	AutoRenew bool      `json:"auto_renew"`
	NotAfter  time.Time `json:"not_after"`
	UpdatedAt time.Time `json:"updated_at"`

	CertPEM []byte `json:"-"`
	KeyPEM  []byte `json:"-"` // 明文只在装配下发载荷时出现
}

// CertNode 是节点回执：这张证书在那台机器上到底加载了没有。
type CertNode struct {
	Domain      string    `json:"domain"`
	NodeID      string    `json:"node_id"`
	NotAfter    time.Time `json:"not_after"`
	Fingerprint string    `json:"fingerprint"`
	ReportedAt  time.Time `json:"reported_at"`
}

// ListCerts 读出全部证书。sealer 为 nil 时不解私钥——
// 读接口（GET /certs）不需要它，而私钥不该在不必要的地方出现。
func (s *Store) ListCerts(ctx context.Context, sealer *secret.Sealer) ([]Cert, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT domain, issuer, challenge, auto_renew, cert_pem, key_pem, not_after, updated_at
		 FROM certs ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Cert
	for rows.Next() {
		var c Cert
		var sealed []byte
		if err := rows.Scan(&c.Domain, &c.Issuer, &c.Challenge, &c.AutoRenew,
			&c.CertPEM, &sealed, &c.NotAfter, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if sealer != nil {
			if c.KeyPEM, err = sealer.Open(sealed); err != nil {
				return nil, fmt.Errorf("解开 %s 的私钥: %w", c.Domain, err)
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCert(ctx context.Context, domain string, sealer *secret.Sealer) (Cert, error) {
	var c Cert
	var sealed []byte
	err := s.Pool.QueryRow(ctx,
		`SELECT domain, issuer, challenge, auto_renew, cert_pem, key_pem, not_after, updated_at
		 FROM certs WHERE domain = $1`, domain).
		Scan(&c.Domain, &c.Issuer, &c.Challenge, &c.AutoRenew,
			&c.CertPEM, &sealed, &c.NotAfter, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if sealer != nil {
		c.KeyPEM, err = sealer.Open(sealed)
	}
	return c, err
}

// PutCert 写入一张证书。私钥 AES-GCM 加密落库。
func (s *Store) PutCert(ctx context.Context, c Cert, sealer *secret.Sealer) error {
	if sealer == nil {
		return errors.New("要写入证书私钥，但没有可用的密封器（装配漏了 Sealer）")
	}
	sealed, err := sealer.Seal(c.KeyPEM)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx,
		`INSERT INTO certs (domain, issuer, challenge, auto_renew, cert_pem, key_pem, not_after, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		 ON CONFLICT (domain) DO UPDATE SET
		   issuer = EXCLUDED.issuer, challenge = EXCLUDED.challenge,
		   auto_renew = EXCLUDED.auto_renew, cert_pem = EXCLUDED.cert_pem,
		   key_pem = EXCLUDED.key_pem, not_after = EXCLUDED.not_after, updated_at = now()`,
		c.Domain, c.Issuer, defaultStr(c.Challenge, "dns-01"), c.AutoRenew,
		c.CertPEM, sealed, c.NotAfter)
	return err
}

func (s *Store) DeleteCert(ctx context.Context, domain string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM certs WHERE domain = $1`, domain)
	return err
}

// ReplaceCertReceipts 换掉一个节点报上来的全部回执。
//
// **整体替换而不是逐条 upsert**：一张已经从节点上消失的证书，如果它的旧回执
// 留在库里，证书页会一直显示「这台机器加载了」——而实际上没有。
// 回执描述的是「此刻那台机器上有什么」，不是历史。
func (s *Store) ReplaceCertReceipts(ctx context.Context, nodeID string, receipts []CertNode) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM cert_nodes WHERE node_id = $1`, nodeID); err != nil {
		return err
	}
	for _, r := range receipts {
		// 外键要求 certs 里有这个域名。节点报上来一张主控不认识的证书是可能的
		// （比如上一版留下的），跳过而不是让整批失败。
		if _, err := tx.Exec(ctx,
			`INSERT INTO cert_nodes (domain, node_id, not_after, fingerprint, reported_at)
			 SELECT $1, $2, $3, $4, now() WHERE EXISTS (SELECT 1 FROM certs WHERE domain = $1)`,
			r.Domain, nodeID, r.NotAfter, r.Fingerprint); err != nil {
			return fmt.Errorf("写入 %s 在 %s 上的回执: %w", r.Domain, nodeID, err)
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListCertReceipts(ctx context.Context) ([]CertNode, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT domain, node_id, not_after, fingerprint, reported_at
		 FROM cert_nodes ORDER BY domain, node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CertNode
	for rows.Next() {
		var r CertNode
		if err := rows.Scan(&r.Domain, &r.NodeID, &r.NotAfter, &r.Fingerprint, &r.ReportedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
