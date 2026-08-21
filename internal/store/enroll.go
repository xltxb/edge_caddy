package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrTokenInvalid = errors.New("接入 Token 无效")
	ErrTokenExpired = errors.New("接入 Token 已过期")
	ErrTokenUsed    = errors.New("接入 Token 已被使用")
)

// EnrollTokenTTL —— 30 分钟。一条泄漏在聊天记录里的安装命令不该永远可用。
const EnrollTokenTTL = 30 * time.Minute

// NodeSpec 是签发 Token 时就绑定的机器身份。
// 绑定在 Token 上而不是等节点接入后自报：自报意味着一条命令可以被拿去
// 给任意一台机器用，而 Token 本来就该是「这一台」的凭证。
type NodeSpec struct {
	NodeID   string
	City     string
	Vendor   string
	Line     string
	PublicIP string
}

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// IssueEnrollToken 返回明文 Token，库里只留哈希。
// 明文仅在这一次返回里出现，任何后续接口都不回显（PRD §7）。
func (s *Store) IssueEnrollToken(ctx context.Context, spec NodeSpec) (string, time.Time, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("生成 Token: %w", err)
	}
	plain := "ec_" + hex.EncodeToString(b[:])
	expires := time.Now().Add(EnrollTokenTTL)

	_, err := s.Pool.Exec(ctx,
		`INSERT INTO enroll_tokens (token_hash, node_id, city, vendor, line, public_ip, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		hashToken(plain), spec.NodeID, spec.City, spec.Vendor, spec.Line, spec.PublicIP, expires)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("保存 Token: %w", err)
	}
	return plain, expires, nil
}

// ConsumeEnrollToken 原子地把 Token 标记为已用并返回它绑定的身份。
//
// 用一条带条件的 UPDATE 而不是「先查再改」：接入是网络上的操作，
// 同一条命令被跑两遍完全可能，先查再改会让两次都通过，产生两个身份。
// PeekEnrollToken 查验一张接入 Token，**不消耗它**。
//
// 拆出这一步是因为查验之后还有好几件会失败的事：节点已被下线、写入节点失败、
// 签发隧道证书失败。查验与消耗一体的话，**那几件事失败也会把 Token 烧掉**——
// 而后两件是主控自己的内部错误，凭什么让用户的 Token 作废？
//
// 人这时要回控制台重签一张，而错误只出现在那台机器的日志里。
func (s *Store) PeekEnrollToken(ctx context.Context, plain string) (NodeSpec, error) {
	var spec NodeSpec
	var usedAt *time.Time
	var expiresAt time.Time

	err := s.Pool.QueryRow(ctx,
		`SELECT node_id, city, vendor, line, host(public_ip), used_at, expires_at
		 FROM enroll_tokens WHERE token_hash = $1`, hashToken(plain)).
		Scan(&spec.NodeID, &spec.City, &spec.Vendor, &spec.Line, &spec.PublicIP, &usedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return spec, ErrTokenInvalid
	}
	if err != nil {
		return spec, err
	}
	if usedAt != nil {
		return spec, ErrTokenUsed
	}
	if time.Now().After(expiresAt) {
		return spec, ErrTokenExpired
	}
	return spec, nil
}

// ConsumeEnrollToken 把一张 Token 标记为已用。
//
// **在接入真正成功之后调用**：这一步之后没有任何会失败的事，
// 所以一次消耗就对应一次真实的接入。
//
// 它自己是原子的（UPDATE ... WHERE used_at IS NULL），所以 Peek 与 Consume
// 之间那个窗口不会让同一张 Token 被用两次——抢输的那一方拿到 ErrTokenUsed。
func (s *Store) ConsumeEnrollToken(ctx context.Context, plain string) error {
	tag, err := s.Pool.Exec(ctx,
		`UPDATE enroll_tokens SET used_at = now()
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()`, hashToken(plain))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// 有人在 Peek 与这里之间抢先用掉了，或者它刚好过期。
		return ErrTokenUsed
	}
	return nil
}
