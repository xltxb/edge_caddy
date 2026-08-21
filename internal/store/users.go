package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

var ErrNoSession = errors.New("会话不存在或已过期")

func (s *Store) CreateUser(ctx context.Context, username, password string) error {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("哈希口令: %w", err)
	}
	_, err = s.Pool.Exec(ctx,
		`INSERT INTO users (username, pw_hash) VALUES ($1, $2)
		 ON CONFLICT (username) DO UPDATE SET pw_hash = EXCLUDED.pw_hash`,
		username, string(h))
	return err
}

// VerifyPassword 对不存在的用户也走一次 bcrypt 比较。
//
// 不这么做的话，「用户不存在」会立刻返回而「口令错误」要等约 60ms，
// 于是响应时间本身就成了用户名枚举的信道——而 §1 特意规定了
// 登录失败不区分这两种情况，只在响应文案上区分是不够的。
func (s *Store) VerifyPassword(ctx context.Context, username, password string) bool {
	var hash string
	err := s.Pool.QueryRow(ctx, `SELECT pw_hash FROM users WHERE username = $1`, username).Scan(&hash)
	if err != nil {
		// 一个固定的 bcrypt 哈希，仅用于消耗与真实比较相当的时间。
		hash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil && err == nil
}

func (s *Store) CreateSession(ctx context.Context, username, srcIP string, ttl time.Duration) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成会话 id: %w", err)
	}
	id := hex.EncodeToString(b[:])

	var ip any
	if srcIP != "" {
		ip = srcIP
	}
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO sessions (id, username, src_ip, expires_at) VALUES ($1, $2, $3, $4)`,
		id, username, ip, time.Now().Add(ttl))
	if err != nil {
		return "", fmt.Errorf("写会话: %w", err)
	}
	return id, nil
}

func (s *Store) SessionOwner(ctx context.Context, id string) (string, error) {
	var username string
	err := s.Pool.QueryRow(ctx,
		`SELECT username FROM sessions WHERE id = $1 AND expires_at > now()`, id).Scan(&username)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoSession
	}
	if err != nil {
		return "", err
	}
	return username, nil
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}
