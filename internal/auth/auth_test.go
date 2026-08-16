package auth_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/auth"
	"github.com/xltxb/edge_caddy/internal/store"
)

func newManager(t *testing.T) (*auth.Manager, context.Context) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return auth.New(st), context.Background()
}

// 尚未设置口令时，鉴权处于关闭状态。
//
// 这是设计稿登录页写明的首次部署行为（"未设置时接口无鉴权"）。把它固化成测试
// 是因为它是个**危险的默认值**：一台刚起来的主控是敞开的。要它成为一个人有意
// 选择的状态，而不是某次重构后谁也没注意到的副作用。
func TestAuthDisabledUntilPasswordSet(t *testing.T) {
	m, ctx := newManager(t)
	if m.Enabled(ctx) {
		t.Fatal("尚未设置口令时鉴权应为关闭")
	}
	if err := m.SetPassword(ctx, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if !m.Enabled(ctx) {
		t.Fatal("设置口令后鉴权应自动启用")
	}
}

func TestLoginRejectsWrongCredentials(t *testing.T) {
	m, ctx := newManager(t)
	if err := m.SetPassword(ctx, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct{ user, pass string }{
		"口令错":  {auth.AdminUser, "wrong"},
		"用户名错": {"root", "correct horse battery staple"},
		"两者都错": {"root", "wrong"},
		"口令为空": {auth.AdminUser, ""},
	} {
		if _, err := m.Login(ctx, c.user, c.pass); !errors.Is(err, auth.ErrBadCredential) {
			t.Errorf("%s 应返回 ErrBadCredential，实际 %v", name, err)
		}
	}
}

// 用户名错与口令错返回**同一个**错误。
//
// 区分它们会把「这个用户名存在」泄漏给爆破者，让他先枚举用户名再集中猜口令。
func TestWrongUserAndWrongPasswordAreIndistinguishable(t *testing.T) {
	m, ctx := newManager(t)
	_ = m.SetPassword(ctx, "correct horse battery staple")

	_, errUser := m.Login(ctx, "root", "correct horse battery staple")
	_, errPass := m.Login(ctx, auth.AdminUser, "wrong")
	if errUser.Error() != errPass.Error() {
		t.Fatalf("两种失败的错误信息不应可区分:\n  用户名错: %v\n  口令错:   %v", errUser, errPass)
	}
}

func TestLoginIssuesUsableSession(t *testing.T) {
	m, ctx := newManager(t)
	_ = m.SetPassword(ctx, "correct horse battery staple")

	sid, err := m.Login(ctx, auth.AdminUser, "correct horse battery staple")
	if err != nil {
		t.Fatalf("正确凭据应登录成功: %v", err)
	}
	if sid == "" {
		t.Fatal("登录应返回会话 ID")
	}
	if user := m.UserBySession(sid); user != auth.AdminUser {
		t.Fatalf("会话应能解析回用户，实际 %q", user)
	}
	if user := m.UserBySession("bogus"); user != "" {
		t.Fatalf("无效会话不应解析出用户，实际 %q", user)
	}
}

func TestSessionExpires(t *testing.T) {
	m, ctx := newManager(t)
	_ = m.SetPassword(ctx, "correct horse battery staple")

	now := time.Now()
	m.SetClock(func() time.Time { return now })
	sid, err := m.Login(ctx, auth.AdminUser, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if m.UserBySession(sid) == "" {
		t.Fatal("刚签发的会话应有效")
	}

	m.SetClock(func() time.Time { return now.Add(auth.SessionTTL + time.Second) })
	if user := m.UserBySession(sid); user != "" {
		t.Fatalf("过期会话不应再解析出用户，实际 %q", user)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	m, ctx := newManager(t)
	_ = m.SetPassword(ctx, "correct horse battery staple")
	sid, _ := m.Login(ctx, auth.AdminUser, "correct horse battery staple")

	m.Logout(sid)
	if user := m.UserBySession(sid); user != "" {
		t.Fatalf("登出后会话应失效，实际 %q", user)
	}
	// 登出是幂等的：重复登出、登出不存在的会话都不该 panic
	m.Logout(sid)
	m.Logout("never-existed")
}

// 口令改了之后，已签发的会话必须失效。
//
// 改口令的常见动机就是「怀疑口令泄漏了」。若旧会话继续有效，攻击者手上那个
// 会话照样能用，改口令这个动作等于没做。
func TestChangingPasswordInvalidatesExistingSessions(t *testing.T) {
	m, ctx := newManager(t)
	_ = m.SetPassword(ctx, "old password here")
	sid, _ := m.Login(ctx, auth.AdminUser, "old password here")
	if m.UserBySession(sid) == "" {
		t.Fatal("前置条件：会话应有效")
	}

	if err := m.SetPassword(ctx, "new password here"); err != nil {
		t.Fatal(err)
	}
	if user := m.UserBySession(sid); user != "" {
		t.Fatalf("改口令后旧会话应失效，实际 %q", user)
	}
}

// 库里存的必须是哈希，不能是明文口令。
func TestPasswordIsNotStoredInPlaintext(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "a.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	const pw = "correct horse battery staple"
	if err := auth.New(st).SetPassword(ctx, pw); err != nil {
		t.Fatal(err)
	}

	raw, err := st.GetSetting(ctx, auth.KeyAdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == pw {
		t.Fatal("口令以明文落库了")
	}
	if len(raw) == 0 {
		t.Fatal("口令没有落库")
	}
}
