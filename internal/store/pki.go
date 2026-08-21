package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/xltxb/edge_caddy/internal/pki"
	"github.com/xltxb/edge_caddy/internal/secret"
)

// EnsureCA 读出某一套 CA；不存在就生成一套并落库。
//
// 自动生成而不是要求人先跑一条建 CA 的命令：一个必须手工初始化才能工作的控制面，
// 会在「重装了一台主控」的那天以「节点全都连不上」的形式失败，而那时没人会想到
// 是因为少跑了一条命令。
//
// 根私钥以 AES-GCM 密文落库，任何接口不回显（PRD §7）。
func (s *Store) EnsureCA(ctx context.Context, kind pki.Kind, sealer *secret.Sealer) (*pki.CA, error) {
	var certPEM, sealedKey []byte
	err := s.Pool.QueryRow(ctx,
		`SELECT cert_pem, key_pem FROM pki_cas WHERE kind = $1`, string(kind)).
		Scan(&certPEM, &sealedKey)

	switch {
	case err == nil:
		keyPEM, err := sealer.Open(sealedKey)
		if err != nil {
			// 密钥换了或密文损坏。这里必须硬失败：静默重新生成一套 CA 会让
			// 全部已接入节点的证书一夜之间失效，而现象是「所有节点同时掉线」。
			return nil, fmt.Errorf("解开 %s CA 私钥失败（EC_SECRET_KEY 是否变过？）: %w", kind, err)
		}
		return pki.LoadCA(kind, certPEM, keyPEM)

	case errors.Is(err, pgx.ErrNoRows):
		ca, err := pki.GenerateCA(kind)
		if err != nil {
			return nil, err
		}
		sealed, err := sealer.Seal(ca.KeyPEM)
		if err != nil {
			return nil, err
		}
		// ON CONFLICT DO NOTHING + 回读：两个主控实例同时首启时，
		// 谁先写谁赢，输的那个用赢家的 CA，而不是覆盖掉它。
		tag, err := s.Pool.Exec(ctx,
			`INSERT INTO pki_cas (kind, cert_pem, key_pem) VALUES ($1, $2, $3)
			 ON CONFLICT (kind) DO NOTHING`, string(kind), ca.CertPEM, sealed)
		if err != nil {
			return nil, fmt.Errorf("保存 %s CA: %w", kind, err)
		}
		if tag.RowsAffected() == 0 {
			return s.EnsureCA(ctx, kind, sealer)
		}
		return ca, nil

	default:
		return nil, fmt.Errorf("读取 %s CA: %w", kind, err)
	}
}
