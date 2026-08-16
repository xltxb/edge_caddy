// Package enroll 管理边缘节点的接入凭据。
//
// 接入流程（docs/adr/0009）：节点首连时还没有隧道证书，因此不能用 mTLS 客户端认证。
// 首连走服务端单向 TLS + 一次性 Token，主控在这次交换中签发并下发隧道客户端证书；
// 此后该节点的所有连接都用 mTLS，Token 不再参与。
package enroll

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xltxb/edge_caddy/internal/store"
)

var (
	ErrNoToken      = errors.New("缺少接入 Token")
	ErrUnknownToken = errors.New("接入 Token 无效")
	ErrTokenUsed    = errors.New("接入 Token 已被使用")
	ErrTokenExpired = errors.New("接入 Token 已过期")
	ErrNoNodeID     = errors.New("缺少节点 ID")
)

// TokenTTL 是接入 Token 的默认有效期（后端文档 §4）。
const TokenTTL = 30 * time.Minute

// Store 是 enroll 需要的存储能力。
//
// 直接用 store.TokenState 而不是另立一个结构：enroll 本来就是 store 之上的策略层，
// 为「解耦」再复制一份同形状的类型只会让两边悄悄漂开。
type Store interface {
	PutEnrollToken(ctx context.Context, hash string, expiresAt time.Time) error
	ConsumeEnrollToken(ctx context.Context, hash, nodeID string, now time.Time) (bool, error)
	EnrollTokenState(ctx context.Context, hash string, now time.Time) (store.TokenState, error)
}

type Enroller struct {
	st  Store
	now func() time.Time
}

func New(st Store) *Enroller {
	return &Enroller{st: st, now: time.Now}
}

// Issue 签发一个新的一次性接入 Token，返回明文与到期时间。
//
// 明文只在这一次返回，库里只存哈希——拿到库的人不该能直接拿它去接入。
// 用 SHA-256 而非 bcrypt：Token 是高熵随机串，不像人选的口令那样可被字典穷举。
func (e *Enroller) Issue(ctx context.Context, ttl time.Duration) (string, time.Time, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("生成接入 Token: %w", err)
	}
	token := "enr_" + hex.EncodeToString(b[:])
	expires := e.now().Add(ttl)
	if err := e.st.PutEnrollToken(ctx, hashToken(token), expires); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Consume 校验并**作废**一个接入 Token，把它绑定到 nodeID。
//
// 作废与校验是同一个原子操作（存储层的条件更新），不是先查后写：
// 安装命令会被复制粘贴，两台机器同时执行时先查后写会让两台都通过。
func (e *Enroller) Consume(ctx context.Context, token, nodeID string) error {
	if strings.TrimSpace(token) == "" {
		return ErrNoToken
	}
	if strings.TrimSpace(nodeID) == "" {
		return ErrNoNodeID
	}
	now := e.now()
	hash := hashToken(token)

	ok, err := e.st.ConsumeEnrollToken(ctx, hash, nodeID, now)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}

	// 走到这里说明没消费成功。再查一次只为给出准确原因——原子性已经由上面那步保证，
	// 这次查询即便与别的消费竞争也不会影响判定结果，最多是原因描述略滞后。
	st, err := e.st.EnrollTokenState(ctx, hash, now)
	if err != nil {
		return err
	}
	switch {
	case !st.Exists:
		return ErrUnknownToken
	case st.ConsumedBy != "":
		return fmt.Errorf("%w（已绑定到 %s）", ErrTokenUsed, st.ConsumedBy)
	case st.Expired:
		return ErrTokenExpired
	default:
		// 理论上到不了：存在、未过期、未被消费，条件更新就该成功。
		// 真到了说明存储层的判定与这里的判定不一致，必须显式报错而不是放行。
		return fmt.Errorf("接入 Token 状态异常，拒绝接入")
	}
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}
