package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/xltxb/edge_caddy/internal/model"
)

// CreateDeploy 写入一次下发的记录，返回自增 ID。
func (s *Store) CreateDeploy(ctx context.Context, d model.Deploy) (int64, error) {
	keys, err := encodeJSON(nonNil(d.ResKeys))
	if err != nil {
		return 0, err
	}
	snap, err := encodeJSON(d.Snapshot)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO deploys (cfg_version, operator, res_keys, snapshot, ok_count, fail_count, created_at)
		VALUES (?, ?, ?, ?, 0, 0, ?)`,
		d.CfgVersion, d.Operator, keys, snap, encodeTime(d.CreatedAt))
	if err != nil {
		return 0, fmt.Errorf("写入下发记录: %w", err)
	}
	return res.LastInsertId()
}

// PutDeployResult 记录某节点的下发结果，并同步刷新该次下发的成功/失败计数。
//
// 计数由结果表现算而不是各处累加：累加要求「每个结果只写一次」，
// 而重推、重试都会让同一节点再写一次结果，累加就会漂。
func (s *Store) PutDeployResult(ctx context.Context, r model.DeployResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // 提交后回滚是空操作

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deploy_results (deploy_id, node_id, state, detail) VALUES (?, ?, ?, ?)
		ON CONFLICT(deploy_id, node_id) DO UPDATE SET state=excluded.state, detail=excluded.detail`,
		r.DeployID, r.NodeID, r.State, r.Detail); err != nil {
		return fmt.Errorf("写入节点结果 %s: %w", r.NodeID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE deploys SET
			ok_count   = (SELECT COUNT(*) FROM deploy_results WHERE deploy_id = ? AND state = 'ok'),
			fail_count = (SELECT COUNT(*) FROM deploy_results WHERE deploy_id = ? AND state = 'fail')
		WHERE id = ?`, r.DeployID, r.DeployID, r.DeployID); err != nil {
		return fmt.Errorf("刷新下发计数: %w", err)
	}
	return tx.Commit()
}

// ListDeploys 倒序返回下发记录及其逐节点结果。
func (s *Store) ListDeploys(ctx context.Context, limit int) ([]model.Deploy, map[int64][]model.DeployResult, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, cfg_version, operator, res_keys, snapshot, ok_count, fail_count, created_at
		FROM deploys ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("查询下发记录: %w", err)
	}
	defer rows.Close()

	out := make([]model.Deploy, 0, limit)
	ids := make([]any, 0, limit)
	for rows.Next() {
		var (
			d          model.Deploy
			keys, snap string
			at         string
		)
		if err := rows.Scan(&d.ID, &d.CfgVersion, &d.Operator, &keys, &snap,
			&d.OKCount, &d.FailCount, &at); err != nil {
			return nil, nil, fmt.Errorf("读取下发记录行: %w", err)
		}
		if err := decodeJSON(keys, &d.ResKeys); err != nil {
			return nil, nil, err
		}
		if err := decodeJSON(snap, &d.Snapshot); err != nil {
			return nil, nil, err
		}
		d.CreatedAt = decodeTime(at)
		out = append(out, d)
		ids = append(ids, d.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(ids) == 0 {
		return out, map[int64][]model.DeployResult{}, nil
	}

	byDeploy, err := s.resultsFor(ctx, ids)
	return out, byDeploy, err
}

func (s *Store) resultsFor(ctx context.Context, ids []any) (map[int64][]model.DeployResult, error) {
	q := `SELECT deploy_id, node_id, state, detail FROM deploy_results WHERE deploy_id IN (?` +
		repeatComma(len(ids)-1) + `) ORDER BY node_id`
	rows, err := s.db.QueryContext(ctx, q, ids...)
	if err != nil {
		return nil, fmt.Errorf("查询逐节点结果: %w", err)
	}
	defer rows.Close()

	out := map[int64][]model.DeployResult{}
	for rows.Next() {
		var r model.DeployResult
		if err := rows.Scan(&r.DeployID, &r.NodeID, &r.State, &r.Detail); err != nil {
			return nil, fmt.Errorf("读取节点结果行: %w", err)
		}
		out[r.DeployID] = append(out[r.DeployID], r)
	}
	return out, rows.Err()
}

func repeatComma(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += ",?"
	}
	return s
}

// GetDeployByVersion 按配置版本号取下发记录，回滚要用它的快照。
func (s *Store) GetDeployByVersion(ctx context.Context, cfg string) (model.Deploy, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, cfg_version, operator, res_keys, snapshot, ok_count, fail_count, created_at
		FROM deploys WHERE cfg_version = ?`, cfg)
	var (
		d          model.Deploy
		keys, snap string
		at         string
	)
	err := row.Scan(&d.ID, &d.CfgVersion, &d.Operator, &keys, &snap, &d.OKCount, &d.FailCount, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Deploy{}, ErrNotFound
	}
	if err != nil {
		return model.Deploy{}, fmt.Errorf("查询下发记录 %s: %w", cfg, err)
	}
	if err := decodeJSON(keys, &d.ResKeys); err != nil {
		return model.Deploy{}, err
	}
	if err := decodeJSON(snap, &d.Snapshot); err != nil {
		return model.Deploy{}, err
	}
	d.CreatedAt = decodeTime(at)
	return d, nil
}

// Baseline 返回当前基线版本号，即最近一次下发的版本。尚无下发时返回空串。
func (s *Store) Baseline(ctx context.Context) (string, error) {
	var cfg string
	err := s.db.QueryRowContext(ctx,
		`SELECT cfg_version FROM deploys ORDER BY id DESC LIMIT 1`).Scan(&cfg)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("查询基线: %w", err)
	}
	return cfg, nil
}
