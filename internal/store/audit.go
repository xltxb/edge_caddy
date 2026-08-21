package store

import "context"

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
