package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Deploy 是一次下发的记录。snapshot 是本次全量渲染的快照，回滚以它为源。
type Deploy struct {
	ID         int64           `json:"id"`
	CfgVersion string          `json:"cfg_version"`
	Operator   string          `json:"operator"`
	ResKeys    []string        `json:"res_keys"`
	OKCount    int             `json:"ok_count"`
	FailCount  int             `json:"fail_count"`
	Targets    []string        `json:"targets"`
	IsBaseline bool            `json:"is_baseline"`
	CreatedAt  time.Time       `json:"created_at"`
	Snapshot   json.RawMessage `json:"-"`
}

// DeployResult 是一次下发在单个节点上的结果。
//
// Retrying 记的是「这一行还会不会再动」。ADR-0005：节点未回应才重试，
// 节点回应了但 Caddy 拒绝则不重试。前端据此决定显示「重试中」还是终态红字。
type DeployResult struct {
	Node     string `json:"node"`
	State    string `json:"state"` // ok | fail
	Detail   string `json:"detail"`
	Retrying bool   `json:"retrying"`
}

// NewCfgVersion 生成一个基线版本号，形如 cfg-2f9a1c。
func NewCfgVersion() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return "cfg-" + hex.EncodeToString(b[:])
}

// CreateDeploy 记下一次下发。targets 是本次**应当**回报的节点列表。
//
// 存列表而不是只存个数：个数说得出「少了几个」，说不出「少的是哪几个」。
// 用户在下发进行中刷新页面时，前端手上没有 POST /deploys 那次响应里的目标列表，
// 只有从这里读回来才画得出「待下发」的那几行——而「还有谁没回来」正是断线降级时
// 最需要看见的信息。
func (s *Store) CreateDeploy(ctx context.Context, cfgVersion, operator string, resKeys []string, snapshot []byte, targets []string) (int64, error) {
	keys, err := json.Marshal(defaultSlice(resKeys))
	if err != nil {
		return 0, err
	}
	tg, err := json.Marshal(defaultSlice(targets))
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.Pool.QueryRow(ctx,
		`INSERT INTO deploys (cfg_version, operator, res_keys, snapshot, targets)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		cfgVersion, operator, keys, snapshot, tg).Scan(&id)
	return id, err
}

func (s *Store) SaveDeployResult(ctx context.Context, deployID int64, r DeployResult) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO deploy_results (deploy_id, node_id, state, detail, retrying)
		 VALUES ($1,$2,$3::deploy_state,$4,$5)
		 ON CONFLICT (deploy_id, node_id) DO UPDATE SET
		   state = EXCLUDED.state, detail = EXCLUDED.detail, retrying = EXCLUDED.retrying`,
		deployID, r.Node, r.State, r.Detail, r.Retrying)
	return err
}

func (s *Store) FinishDeploy(ctx context.Context, deployID int64, okCount, failCount int) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE deploys SET ok_count = $2, fail_count = $3 WHERE id = $1`,
		deployID, okCount, failCount)
	return err
}

// SetBaseline 确立新基线。单行表，用 upsert。
//
// 基线是「当前应当在所有边缘节点上生效的那一版」（CONTEXT.md）。它只由主控知道，
// 前端反推不出来——从节点上报的版本取众数，在一次下发只到了少数节点时会指向旧版，
// 于是配置漂移会反着算。
func (s *Store) SetBaseline(ctx context.Context, cfgVersion string, deployID int64) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO baseline (only_row, cfg_version, deploy_id) VALUES (TRUE, $1, $2)
		 ON CONFLICT (only_row) DO UPDATE SET
		   cfg_version = EXCLUDED.cfg_version, deploy_id = EXCLUDED.deploy_id`,
		cfgVersion, deployID)
	return err
}

// Baseline 返回当前基线；从未成功下发过时返回空串。
func (s *Store) Baseline(ctx context.Context) (string, error) {
	var v string
	err := s.Pool.QueryRow(ctx, `SELECT cfg_version FROM baseline WHERE only_row`).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) GetDeploy(ctx context.Context, id int64) (Deploy, []DeployResult, error) {
	var d Deploy
	var keys, tg []byte
	err := s.Pool.QueryRow(ctx,
		`SELECT d.id, d.cfg_version, d.operator, d.res_keys, d.ok_count, d.fail_count, d.targets, d.created_at,
		        (b.cfg_version IS NOT NULL AND b.cfg_version = d.cfg_version) AS is_baseline
		 FROM deploys d LEFT JOIN baseline b ON TRUE
		 WHERE d.id = $1`, id).
		Scan(&d.ID, &d.CfgVersion, &d.Operator, &keys, &d.OKCount, &d.FailCount, &tg, &d.CreatedAt, &d.IsBaseline)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, nil, ErrNotFound
	}
	if err != nil {
		return d, nil, err
	}
	if err := json.Unmarshal(keys, &d.ResKeys); err != nil {
		return d, nil, err
	}
	if err := json.Unmarshal(tg, &d.Targets); err != nil {
		return d, nil, err
	}

	rows, err := s.Pool.Query(ctx,
		`SELECT node_id, state::text, detail, retrying FROM deploy_results
		 WHERE deploy_id = $1 ORDER BY node_id`, id)
	if err != nil {
		return d, nil, err
	}
	defer rows.Close()

	var results []DeployResult
	for rows.Next() {
		var r DeployResult
		if err := rows.Scan(&r.Node, &r.State, &r.Detail, &r.Retrying); err != nil {
			return d, nil, err
		}
		results = append(results, r)
	}
	return d, results, rows.Err()
}

func (s *Store) ListDeploys(ctx context.Context, limit int, beforeID int64) ([]Deploy, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT d.id, d.cfg_version, d.operator, d.res_keys, d.ok_count, d.fail_count, d.targets, d.created_at,
	             (b.cfg_version IS NOT NULL AND b.cfg_version = d.cfg_version) AS is_baseline
	      FROM deploys d LEFT JOIN baseline b ON TRUE
	      WHERE ($2 = 0 OR d.id < $2)
	      ORDER BY d.id DESC LIMIT $1`
	rows, err := s.Pool.Query(ctx, q, limit, beforeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Deploy
	for rows.Next() {
		var d Deploy
		var keys, tg []byte
		if err := rows.Scan(&d.ID, &d.CfgVersion, &d.Operator, &keys,
			&d.OKCount, &d.FailCount, &tg, &d.CreatedAt, &d.IsBaseline); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(keys, &d.ResKeys); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(tg, &d.Targets); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecountDeploy 按 deploy_results 重算成功/失败数。
//
// 重试会让这两个数字在下发返回之后继续变，因此它们不能在写入时累加——
// 累加会在重试成功时留下一个「既算失败又算成功」的旧账。
func (s *Store) RecountDeploy(ctx context.Context, deployID int64) (ok, fail int, err error) {
	err = s.Pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE state = 'ok'),
		        count(*) FILTER (WHERE state = 'fail')
		 FROM deploy_results WHERE deploy_id = $1`, deployID).Scan(&ok, &fail)
	if err != nil {
		return 0, 0, err
	}
	_, err = s.Pool.Exec(ctx,
		`UPDATE deploys SET ok_count = $2, fail_count = $3 WHERE id = $1`, deployID, ok, fail)
	return ok, fail, err
}

// ClearStaleRetries 把「重试中」的结果标为终态。
//
// 主控重启时内存里的重试队列就没了，而库里那些 retrying=true 的行会让
// phase 永远停在 running——弹层永远不落定，而它等的那个重试再也不会发生。
// 启动时清一遍，宁可说「中断了」也不要一个永远转圈的界面。
func (s *Store) ClearStaleRetries(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx,
		`UPDATE deploy_results
		 SET retrying = FALSE,
		     detail = CASE WHEN detail = '' THEN '主控重启，重试已中断'
		                   ELSE detail || '（主控重启，重试已中断）' END
		 WHERE retrying`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
