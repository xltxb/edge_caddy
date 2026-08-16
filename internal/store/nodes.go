package store

import (
	"context"
	"fmt"
	"time"

	"github.com/xltxb/edge_caddy/internal/model"
)

// UpsertNodeSeen 记录一次节点心跳。
//
// 节点首次接入时这里会创建记录：节点的存在由它自己连上来证明，
// 而不是由人先在面板上登记——后者会产生一堆「登记了但从没连上」的幽灵节点。
func (s *Store) UpsertNodeSeen(ctx context.Context, id, cfgVersion string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO edge_nodes (id, status, cfg_version, last_hb_at, created_at)
		VALUES (?, 'ok', ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status='ok', cfg_version=excluded.cfg_version, last_hb_at=excluded.last_hb_at`,
		id, cfgVersion, encodeTime(now), encodeTime(now))
	if err != nil {
		return fmt.Errorf("记录节点 %s 心跳: %w", id, err)
	}
	return nil
}

// MarkNodeDown 把节点标记为离线。
func (s *Store) MarkNodeDown(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE edge_nodes SET status='down' WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("标记节点 %s 离线: %w", id, err)
	}
	return nil
}

func (s *Store) ListNodes(ctx context.Context) ([]model.Node, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, city, vendor, line, public_ip, status, cfg_version, dns_enabled,
		       COALESCE(last_hb_at,''), created_at
		FROM edge_nodes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("查询节点: %w", err)
	}
	defer rows.Close()

	out := make([]model.Node, 0, 8)
	for rows.Next() {
		var (
			n         model.Node
			dns       int
			hb, birth string
		)
		if err := rows.Scan(&n.ID, &n.City, &n.Vendor, &n.Line, &n.PublicIP,
			&n.Status, &n.CfgVersion, &dns, &hb, &birth); err != nil {
			return nil, fmt.Errorf("读取节点行: %w", err)
		}
		n.DNSEnabled = dns == 1
		n.LastHB = decodeTime(hb)
		n.CreatedAt = decodeTime(birth)
		out = append(out, n)
	}
	return out, rows.Err()
}
