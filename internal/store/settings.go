package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetSetting 读取一个设置项。不存在时返回 ErrNotFound。
func (s *Store) GetSetting(ctx context.Context, key string) ([]byte, error) {
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT v FROM settings WHERE k = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取设置 %s: %w", key, err)
	}
	return v, nil
}

// PutSetting 写入一个设置项。值按字节存，加密与否由调用方决定
// （PKI 私钥等敏感值在 internal/pki 里加密后才到这里）。
func (s *Store) PutSetting(ctx context.Context, key string, val []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`,
		key, val)
	if err != nil {
		return fmt.Errorf("写入设置 %s: %w", key, err)
	}
	return nil
}
