package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TokenState 描述一个接入 Token 当前的状态，用于在消费失败时说清原因。
type TokenState struct {
	Exists     bool
	Expired    bool
	ConsumedBy string // 空串表示尚未被消费
}

// PutEnrollToken 登记一个新签发的接入 Token（以哈希存）。
func (s *Store) PutEnrollToken(ctx context.Context, hash string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO enroll_tokens (token_hash, expires_at) VALUES (?, ?)`,
		hash, encodeTime(expiresAt))
	if err != nil {
		return fmt.Errorf("登记接入 Token: %w", err)
	}
	return nil
}

// ConsumeEnrollToken 原子地把一个未使用且未过期的 Token 标记为已被 nodeID 使用。
//
// 成功返回 true。用**条件更新 + 受影响行数**判定，而不是先查后写：
// 安装命令是复制粘贴的，粘到两台机器上同时执行完全可能发生，先查后写会让两台
// 都通过，于是两台机器以同一身份接入，主控再也分不出它们。
func (s *Store) ConsumeEnrollToken(ctx context.Context, hash, nodeID string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE enroll_tokens SET consumed_by = ?, consumed_at = ?
		WHERE token_hash = ? AND consumed_by IS NULL AND expires_at > ?`,
		nodeID, encodeTime(now), hash, encodeTime(now))
	if err != nil {
		return false, fmt.Errorf("消费接入 Token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// EnrollTokenState 查询 Token 的状态。只在消费失败后调用，用来给出准确的失败原因。
func (s *Store) EnrollTokenState(ctx context.Context, hash string, now time.Time) (TokenState, error) {
	var (
		exp      string
		consumed sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT expires_at, consumed_by FROM enroll_tokens WHERE token_hash = ?`, hash).
		Scan(&exp, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenState{}, nil
	}
	if err != nil {
		return TokenState{}, fmt.Errorf("查询接入 Token 状态: %w", err)
	}
	return TokenState{
		Exists:     true,
		Expired:    !decodeTime(exp).After(now),
		ConsumedBy: consumed.String,
	}, nil
}
