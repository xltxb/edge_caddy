package caddytest_test

import (
	"os/exec"
	"strings"
	"testing"
)

// **这些 ADR 的前提是「官方 Caddy 没有某某模块」，而那会随版本变。**
//
// 三个决定都建立在同一类事实上：
//
//   - ADR-0001 主控签发证书 —— 官方包没有 DNS provider 模块，节点做不了 DNS-01
//   - ADR-0003 边缘鉴权委托给 Agent —— 官方包没有 JWT / HMAC 模块
//   - render/policy.go 拒绝 rate_limit —— 官方包没有限流模块
//
// 每一处都测了**结论**（我们的代码会那样做），没有一处测**前提**。
// 前端 agent 把这个形状说准了：**结论是代码，前提是世界。**
//
// 而这个前提比大多数更容易过期：升一次 Caddy 就可能变。真变了的话，
// 我们会继续拒绝一个已经可用的功能，而拒绝的理由文本还写着「官方包没有这个模块」
// —— 一个措辞正确、依据已经失效的报错。
//
// 所以这条测试的作用不是「保证它永远没有」，是**在它有了的那一天让人知道**。
// 它红了不代表代码错了，代表那三份 ADR 该重判。
func TestOfficialCaddyStillLacksTheModulesWeRoutedAround(t *testing.T) {
	bin, err := exec.LookPath("caddy")
	if err != nil {
		t.Fatalf("找不到 caddy 二进制：%v（其余测试也需要它，不跳过）", err)
	}
	out, err := exec.Command(bin, "list-modules").CombinedOutput()
	if err != nil {
		t.Fatalf("caddy list-modules 失败：%v\n%s", err, out)
	}
	mods := string(out)

	// 逐条对应一个绕路决定。命中就意味着那条绕路可能不必要了。
	absent := []struct {
		needle string
		why    string
	}{
		{"http.handlers.rate_limit", "限流：render/policy.go 因此拒绝 rate_limit=true"},
		{"http.authentication.providers.jwt", "JWT：ADR-0003 因此把验签委托给 Agent"},
		{"dns.providers.", "DNS provider：ADR-0001 因此让主控集中签发证书"},
	}
	for _, a := range absent {
		if strings.Contains(mods, a.needle) {
			t.Errorf("官方 Caddy 现在有 %q 了 —— %s。\n"+
				"这条绕路的前提没了，对应的 ADR 该重新判一次，"+
				"而不是继续照旧执行。", a.needle, a.why)
		}
	}

	// 反向自检：这份输出得真的是模块列表。
	//
	// 不加这一条的话，list-modules 哪天改了输出格式（或者输出到别处），
	// 上面三条会**因为什么都没匹配到而全绿** —— 一个什么也没检查的检查器。
	// 前端刚撞见同一件事：他第一轮扫欠条 11 处命中全是误报，
	// 结论是「一次什么也没找到的扫描，要先证明它不瞎」。
	for _, must := range []string{"http.handlers.reverse_proxy", "http.handlers.static_response"} {
		if !strings.Contains(mods, must) {
			t.Fatalf("模块列表里连 %q 都没有 —— 这份输出不是我们以为的东西，"+
				"上面那三条断言全都没在检查任何东西", must)
		}
	}
}
