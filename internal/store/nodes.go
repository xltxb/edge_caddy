package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Node 是边缘节点在主控这边的记录。
type Node struct {
	ID         string     `json:"id"`
	City       string     `json:"city"`
	Vendor     string     `json:"vendor"`
	Line       string     `json:"line"`
	PublicIP   string     `json:"public_ip"`
	Status     string     `json:"status"`
	CfgVersion string     `json:"cfg_version"`
	DNSEnabled bool       `json:"dns_enabled"`
	LastHBAt   *time.Time `json:"last_hb_at"`
	CreatedAt  time.Time  `json:"created_at"`
	// DrainedAt 非 nil 表示这台机器是**被人下线的**，与 Status 无关（ADR-0014）。
	// nil 时前端不该显示任何下线痕迹 —— 那会跟「它自己挂了」混成一件事。
	DrainedAt *time.Time `json:"drained_at"`
}

// UpsertNode 在接入时写入或更新节点。同一台机器重新接入时更新元信息，
// 不清掉 cfg_version —— 它记的是节点上生效的版本，重新接入并不改变那个事实。
func (s *Store) UpsertNode(ctx context.Context, spec NodeSpec) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO edge_nodes (id, city, vendor, line, public_ip)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (id) DO UPDATE SET
		   city = EXCLUDED.city, vendor = EXCLUDED.vendor,
		   line = EXCLUDED.line, public_ip = EXCLUDED.public_ip`,
		spec.NodeID, spec.City, spec.Vendor, spec.Line, spec.PublicIP)
	return err
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, city, vendor, line, host(public_ip), status::text,
		        cfg_version, dns_enabled, last_hb_at, created_at, drained_at
		 FROM edge_nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.City, &n.Vendor, &n.Line, &n.PublicIP,
			&n.Status, &n.CfgVersion, &n.DNSEnabled, &n.LastHBAt, &n.CreatedAt,
			&n.DrainedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// TouchHeartbeat 记下一次心跳。cfg_version 来自节点上报——它是**节点说的**
// 自己在跑哪一版，主控拿它与基线比对得出配置漂移（ADR-0002）。
//
// status 只在 ok / warn 之间取：判定离线是 health 模块的事，
// 而一次到达的心跳按定义就说明它没离线。
func (s *Store) TouchHeartbeat(ctx context.Context, nodeID, cfgVersion, status string) error {
	if status != "warn" {
		status = "ok"
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes SET last_hb_at = now(), status = $3::node_status, cfg_version = $2
		 WHERE id = $1`, nodeID, cfgVersion, status)
	return err
}

// CountNodesByStatus 返回 ok / warn / down 三档的数量。
//
// **三档由同一条语句产出**，而不是让调用方各算各的：总览上「在线 N · 异常 M ·
// 离线 K」必须等于总数，两处分别推导迟早会算不平——而一处口径错会在界面上
// 冒出来两次，那比单个错数字更让人怀疑整个系统。
func (s *Store) CountNodesByStatus(ctx context.Context) (ok, warn, down, total int, err error) {
	err = s.Pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE status = 'ok'),
		        count(*) FILTER (WHERE status = 'warn'),
		        count(*) FILTER (WHERE status = 'down'),
		        count(*)
		 FROM edge_nodes`).Scan(&ok, &warn, &down, &total)
	return
}

func (s *Store) SetNodeCfgVersion(ctx context.Context, nodeID, cfgVersion string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes SET cfg_version = $2 WHERE id = $1`, nodeID, cfgVersion)
	return err
}

// SetNodeDown 标记节点离线并停掉它的解析。
//
// 两件事一条语句：分开写会出现「已判定离线但还在解析里」的中间态，
// 而流量恰恰在那个窗口里继续往一台死机器上打。
func (s *Store) SetNodeDown(ctx context.Context, nodeID string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes SET status = 'down', dns_enabled = FALSE WHERE id = $1`, nodeID)
	return err
}

// SetNodeDrained 记下或撤销一次下线。
//
// 下线同时关掉解析：这两件事在一条语句里，理由与 SetNodeDown 相同——
// 分开写会出现「已下线但还在解析里」的中间态，而流量恰恰在那个窗口里
// 继续往一台正在退出的机器上打。
//
// **重新上线不自动打开解析。** 一台机器能接入不等于它该马上分流量：
// 它刚回来，配置可能还是旧的。解析由人另外点，或者由下一次成功下发带起来。
func (s *Store) SetNodeDrained(ctx context.Context, nodeID string, drained bool) error {
	if drained {
		_, err := s.Pool.Exec(ctx,
			`UPDATE edge_nodes SET drained_at = now(), dns_enabled = FALSE WHERE id = $1`, nodeID)
		return err
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes SET drained_at = NULL WHERE id = $1`, nodeID)
	return err
}

// IsNodeDrained 回答「这台机器是不是被人下线了」。
//
// 接入路径要用它挡住重连：Agent 断了就重连，不挡的话「关闭隧道」是个假动作。
// 节点不存在时返回 false —— 那是「没见过」，不是「下线了」，
// 两者的处置不同（前者该走正常的接入流程去创建）。
func (s *Store) IsNodeDrained(ctx context.Context, nodeID string) (bool, error) {
	var at *time.Time
	err := s.Pool.QueryRow(ctx,
		`SELECT drained_at FROM edge_nodes WHERE id = $1`, nodeID).Scan(&at)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return at != nil, nil
}

func (s *Store) SetNodeDNS(ctx context.Context, nodeID string, enabled bool) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes SET dns_enabled = $2 WHERE id = $1`, nodeID, enabled)
	return err
}
