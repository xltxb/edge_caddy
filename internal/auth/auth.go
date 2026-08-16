// Package auth 管理控制台的登录与会话。
//
// 只有一个管理员账号（PRD §3：单一角色运维管理员）。会话存在内存里——主控重启
// 需要重新登录，这对一个单人使用的内部控制台是可接受的，也省掉了一张表和它的清理。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/xltxb/edge_caddy/internal/store"
)

var (
	ErrBadCredential = errors.New("用户名或口令不正确")
	ErrNoCredential  = errors.New("尚未设置管理员口令")
)

const (
	// AdminUser 是唯一的管理员用户名。
	AdminUser = "abiu"
	// SessionTTL 是会话有效期。
	SessionTTL = 12 * time.Hour
	// KeyAdminPassword 是口令哈希在设置表里的键。
	KeyAdminPassword = "auth.admin_password"
	// CookieName 是会话 Cookie 的名字。
	CookieName = "edge_session"
)

// Store 是 auth 需要的存储能力。
type Store interface {
	GetSetting(ctx context.Context, key string) ([]byte, error)
	PutSetting(ctx context.Context, key string, val []byte) error
}

type session struct {
	user      string
	expiresAt time.Time
}

type Manager struct {
	st  Store
	now func() time.Time

	mu       sync.RWMutex
	sessions map[string]session
}

func New(st Store) *Manager {
	return &Manager{st: st, now: time.Now, sessions: map[string]session{}}
}

// SetClock 替换时钟，仅供测试注入。
func (m *Manager) SetClock(f func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = f
}

func (m *Manager) clock() func() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.now
}

// Enabled 返回是否已经设置了管理员口令。
//
// 未设置时整个 HTTP 接口是敞开的（设计稿登录页写明的首次部署行为）。
// 这是个危险的默认值，因此调用方在它为 false 时必须显著告警——一台刚起来的
// 主控是敞开的，这件事不该只在文档里提一句。
func (m *Manager) Enabled(ctx context.Context) bool {
	hash, err := m.st.GetSetting(ctx, KeyAdminPassword)
	return err == nil && len(hash) > 0
}

// SetPassword 设置或更换管理员口令，并**作废全部已签发会话**。
//
// 换口令的常见动机就是「怀疑口令泄漏了」。若旧会话继续有效，攻击者手上那个会话
// 照样能用，换口令这个动作等于没做。
func (m *Manager) SetPassword(ctx context.Context, password string) error {
	if len(password) < 8 {
		return fmt.Errorf("口令至少 8 个字符")
	}
	// bcrypt 而非 SHA-256：口令是人选的，熵低，必须靠慢哈希抬高逐个猜测的成本。
	// （接入 Token 那边用 SHA-256 是因为它是高熵随机串，见 internal/enroll。）
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("计算口令哈希: %w", err)
	}
	if err := m.st.PutSetting(ctx, KeyAdminPassword, hash); err != nil {
		return err
	}
	m.mu.Lock()
	m.sessions = map[string]session{}
	m.mu.Unlock()
	return nil
}

// Login 校验凭据并签发会话。
func (m *Manager) Login(ctx context.Context, user, password string) (string, error) {
	hash, err := m.st.GetSetting(ctx, KeyAdminPassword)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNoCredential
	}
	if err != nil {
		return "", err
	}

	// 用户名不匹配也照跑一次 bcrypt：直接返回会让「用户名错」比「口令错」快一个
	// 数量级，构成可测量的用户名枚举信道。两种失败也返回同一个错误——区分它们
	// 等于告诉爆破者哪个用户名存在，让他能先枚举用户名再集中猜口令。
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(AdminUser)) == 1
	passOK := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	if !userOK || !passOK {
		return "", ErrBadCredential
	}

	sid, err := randomToken()
	if err != nil {
		return "", err
	}
	now := m.clock()()
	m.mu.Lock()
	m.sessions[sid] = session{user: AdminUser, expiresAt: now.Add(SessionTTL)}
	m.mu.Unlock()
	return sid, nil
}

// UserBySession 返回会话对应的用户名；无效或已过期时返回空串。
func (m *Manager) UserBySession(sid string) string {
	if sid == "" {
		return ""
	}
	now := m.clock()()
	m.mu.RLock()
	s, ok := m.sessions[sid]
	m.mu.RUnlock()
	if !ok || !now.Before(s.expiresAt) {
		return ""
	}
	return s.user
}

// Logout 作废一个会话。不存在时静默返回——登出是幂等的。
func (m *Manager) Logout(sid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sid)
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成会话 ID: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
