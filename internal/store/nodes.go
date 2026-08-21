package store

import (
	"context"
	"time"
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
		        cfg_version, dns_enabled, last_hb_at, created_at
		 FROM edge_nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.City, &n.Vendor, &n.Line, &n.PublicIP,
			&n.Status, &n.CfgVersion, &n.DNSEnabled, &n.LastHBAt, &n.CreatedAt); err != nil {
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

func (s *Store) SetNodeDNS(ctx context.Context, nodeID string, enabled bool) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE edge_nodes SET dns_enabled = $2 WHERE id = $1`, nodeID, enabled)
	return err
}
