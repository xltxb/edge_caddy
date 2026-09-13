package api_test

import (
	"context"
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
)

// TestASuccessfulLoginDoesNotNeutralizeTheIPCounter：攻击者不能拿自己的账号
// 把 IP 那一档中和掉。
//
// **这条测试盯的是 handleLogin 的调用点，不是限速器内部。**
//
// loginrate_unit_test.go 那条只调 `l.succeed("user:attacker")`——它证明的是
// 「limiter 的 succeed 只清用户名维度」，而真正能出事的地方是 auth.go 往
// succeed 里**传什么**。把那一句改成 `s.logins.succeed(ipKey)` 照样编译、
// 那条单元测试照样绿，而 IP 维度又一次能被一次成功登录抹掉。
//
// 攻击路径（IP 维度存在的全部理由就是挡它）：
//
//	对 victim 试 4 次 → 拿自己的账号成功登录一次 → ip: 条目被删
//	→ 对 victim2 再试 4 次 → … 所有已知用户名各试满五次
//
// 判据是**第六次请求被拒的理由**：到了上限该回 CodeRateLimited，
// 而不是继续回「用户名或密码错误」——后者说明这个 IP 还在被逐一验密码。
func TestASuccessfulLoginDoesNotNeutralizeTheIPCounter(t *testing.T) {
	r, st := newServer(t)
	// 攻击者自己的账号。控制台允许多个账号（store.CreateUser），
	// 这一步不需要任何特权。
	if err := st.CreateUser(context.Background(), "attacker", "attacker-pass"); err != nil {
		t.Fatal(err)
	}

	tryLogin := func(user, pass string) int {
		t.Helper()
		// 同一个来源：httptest 给所有请求同一个 RemoteAddr，
		// 而 IP 维度正是按它分档的。
		_, env := do(t, r, "POST", "/api/v1/auth/login",
			map[string]string{"username": user, "password": pass}, nil)
		return env.Code
	}

	// 四次失败：还差一次到上限。
	for i := 0; i < 4; i++ {
		if got := tryLogin("victim", "wrong"); got != api.CodeBadParam {
			t.Fatalf("装置坏了：第 %d 次失败尝试得到 code=%d，想要 %d", i+1, got, api.CodeBadParam)
		}
	}

	// 攻击者用自己的账号成功登录一次。
	if got := tryLogin("attacker", "attacker-pass"); got != api.CodeOK {
		t.Fatalf("装置坏了：攻击者自己的账号该登得进去，code=%d", got)
	}

	// 第五次失败 —— 这一次还不该被拦（拦的判据是「已经满 5 次」）。
	if got := tryLogin("victim2", "wrong"); got != api.CodeBadParam {
		t.Fatalf("装置坏了：第五次尝试不该被拦，code=%d", got)
	}

	// 第六次：这个 IP 已经失败满五次，该被拦在验密码之前。
	if got := tryLogin("victim3", "wrong"); got != api.CodeRateLimited {
		t.Fatalf("这个 IP 已经失败五次，第六次却得到 code=%d（想要 %d）—— "+
			"一次成功登录把 IP 维度中和掉了，攻击者夹着自己的账号就能把"+
			"所有已知用户名各试满五次", got, api.CodeRateLimited)
	}
}
