package store

import (
	"context"
	"time"
)

// AuditRecord 的 Action 取值见 docs/api-contract.md §5。
// 它由后端产生、在前端页面上原样显示，所以措辞是契约的一部分。
type AuditRecord struct {
	Operator string
	Action   string
	Target   string
	SrcIP    string
	Result   string // ok | fail | partial
	Detail   string
}

func (s *Store) InsertAudit(ctx context.Context, r AuditRecord) error {
	var ip any
	if r.SrcIP != "" {
		ip = r.SrcIP
	}
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO audit_logs (operator, action, target, src_ip, result, detail)
		 VALUES ($1, $2, $3, $4, $5::op_result, $6)`,
		r.Operator, r.Action, r.Target, ip, r.Result, r.Detail)
	return err
}

// AuditEntry 是审计页上的一行。
type AuditEntry struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Operator string    `json:"operator"`
	Action   string    `json:"action"`
	Target   string    `json:"target"`
	SrcIP    string    `json:"src_ip"`
	Result   string    `json:"result"`
	Detail   string    `json:"detail"`
}

// ListAudit 倒序 cursor 分页（api-contract §0.5）。
// 审计是倒序追加的流，用 offset 会在翻页时漏行或重复。
func (s *Store) ListAudit(ctx context.Context, operator string, limit int, beforeID int64) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT id, created_at, operator, action, target,
		        coalesce(host(src_ip),''), result::text, detail
		 FROM audit_logs
		 WHERE ($1 = '' OR operator = $1) AND ($2 = 0 OR id < $2)
		 ORDER BY id DESC LIMIT $3`, operator, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.Operator, &e.Action,
			&e.Target, &e.SrcIP, &e.Result, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
