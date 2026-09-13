package api_test

import (
	"net/http"
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
)

// TestLoginIsRateLimited：连着试错要被拦下来。
//
// 登录原先没有任何限速，而 bcrypt 是暴力破解面前唯一的阻力（issue #33）——
// 那是一道**按 CPU 计价**的防线：它让每次尝试变慢，但没有上限。而控制台
// 按 ADR-0013 是「网络 + 会话」，网络那一半是部署形态，代码里没有任何东西
// 检查它——绑成 0.0.0.0 的主控上，这就是公网上一个没有速率上限的口令接口。
//
// 判据是**第 N+1 次被拦下**（而不是继续跑 bcrypt），且：
//   - 密码对的那一次不受影响
//   - 拦的措辞不能泄漏「这个用户名存在」
func TestLoginIsRateLimited(t *testing.T) {
	r, _ := newServer(t)

	login := func(pw string) (int, int, string) {
		w, env := do(t, r, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"username": "abiu", "password": pw}, nil)
		return w.Code, env.Code, env.Msg
	}

	// 试错到被拦为止。上限是实现细节，这里只要求它**存在**且够小——
	// 一个「允许一万次」的限速与没有限速没有区别。
	var blocked bool
	for i := 0; i < 12; i++ {
		_, code, msg := login("wrong-password")
		if code == api.CodeRateLimited {
			blocked = true
			if containsSub(msg, "abiu") {
				t.Errorf("限速的措辞里带上了用户名：%q —— "+
					"那等于回答了「这个用户名存不存在」", msg)
			}
			break
		}
	}
	if !blocked {
		t.Fatal("连着试错 12 次都没有被拦 —— bcrypt 是这道门上唯一的阻力，" +
			"而它只让每次变慢，没有上限")
	}

	// 被拦之后，**密码对的那一次也进不来**——那正是限速该有的样子：
	// 它拦的是这个来源，不是这次输入。
	if _, code, _ := login("correct-horse"); code == api.CodeOK {
		t.Error("已经被限速了，正确密码却还是登进去了 —— " +
			"那样攻击者只要在试错之间夹一次正确猜测就能绕过")
	}
}

// 没被限速时，正常登录一次就该成功——少了这条，一个「一律拒绝」的实现
// 也能让上面全绿。
func TestLoginStillWorksWhenNotRateLimited(t *testing.T) {
	r, _ := newServer(t)
	_, env := do(t, r, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "abiu", "password": "correct-horse"}, nil)
	if env.Code != api.CodeOK {
		t.Fatalf("正常登录失败了：code=%d msg=%s", env.Code, env.Msg)
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
