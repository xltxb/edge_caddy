package e2e_test

import (
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
	"github.com/xltxb/edge_caddy/internal/caddytest"
)

// setupSite 建一条路由、接一个节点、下发，返回可以直接 curl 的域名。
func setupSite(t *testing.T, r *rig, domain string) {
	t.Helper()
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")
	r.mustDo("POST", "/routes", map[string]any{
		"domain": domain, "upstream": r.upstream, "block_mode": "404",
	})
	r.deployNow("route:" + domain)
}

// TestIPBlacklistActuallyBlocks 是**真请求进真 Caddy** 的验收。
//
// 黑名单与白名单在渲染出来的配置里只差一个 `not`。少写它，「只拦这些」
// 就变成「只放这些」—— **而配置本身完全合法，Caddy 照收，站点看起来也正常**
// （只要访问者恰好在名单里）。所以这条不看配置长什么样，看请求进不进得来。
func TestIPBlacklistActuallyBlocks(t *testing.T) {
	// **必须走 TCP。** unix socket 上 remote_ip 匹配不到任何东西
	// ——实测过：白名单放行回环，走 unix socket 时回 404。
	// 用默认的 rig 的话，这条测试验的是「什么都匹配不到」，而它照样能绿。
	r := newRig(t, caddytest.EdgeTCP())
	setupSite(t, r, "black.example.com")

	// 先确认没规则时通得过 —— **没有这一步，下面的 404 可能是别的原因**。
	if code, _ := r.curlVia("black.example.com"); code != 200 {
		t.Fatalf("装置坏了：还没加规则就通不过（%d）", code)
	}

	// 把测试自己的来源 IP 拉黑。e2e 里 Caddy 与测试同机，来源是回环。
	r.mustDo("PUT", "/rules/bl", map[string]any{
		"name": "拉黑回环", "type": "ip_blacklist", "enabled": true,
		"apply_to": []string{"black.example.com"},
		"spec":     map[string]any{"ips": []string{"127.0.0.0/8", "::1/128"}},
	})
	r.deployNow("rule:bl")

	if code, _ := r.curlVia("black.example.com"); code != 404 {
		t.Errorf("在黑名单里的来源应当被拦（按 block_mode 回 404），实际 %d "+
			"—— 少一个 not 的话黑名单会变成白名单，而配置完全合法", code)
	}
}

// TestIPBlacklistLetsOthersThrough 是上一条的反面。
//
// 没有它，一个「无条件拦」的实现也能让上面全绿 —— 而那会把站点整个封掉。
func TestIPBlacklistLetsOthersThrough(t *testing.T) {
	// **必须走 TCP。** unix socket 上 remote_ip 匹配不到任何东西
	// ——实测过：白名单放行回环，走 unix socket 时回 404。
	// 用默认的 rig 的话，这条测试验的是「什么都匹配不到」，而它照样能绿。
	r := newRig(t, caddytest.EdgeTCP())
	setupSite(t, r, "black2.example.com")

	// 拉黑一个**不是**测试来源的网段。
	r.mustDo("PUT", "/rules/bl2", map[string]any{
		"name": "拉黑别人", "type": "ip_blacklist", "enabled": true,
		"apply_to": []string{"black2.example.com"},
		"spec":     map[string]any{"ips": []string{"203.0.113.0/24"}},
	})
	r.deployNow("rule:bl2")

	if code, _ := r.curlVia("black2.example.com"); code != 200 {
		t.Errorf("不在黑名单里的来源应当照常通过，实际 %d —— "+
			"黑名单写成了白名单的话就是这个症状", code)
	}
}

// TestRequestFilterBlocksByUserAgent 验按请求特征拦。
func TestRequestFilterBlocksByUserAgent(t *testing.T) {
	r := newRig(t)
	setupSite(t, r, "wafish.example.com")

	r.mustDo("PUT", "/rules/ua", map[string]any{
		"name": "挡扫描器", "type": "request_filter", "enabled": true,
		"apply_to": []string{"wafish.example.com"},
		"spec": map[string]any{"filters": []map[string]any{
			{"field": "user_agent", "op": "contains", "value": "sqlmap"},
			{"field": "path", "op": "prefix", "value": "/.git"},
		}},
	})
	r.deployNow("rule:ua")

	// 命中 UA。
	if code, _ := r.caddy.Get("wafish.example.com", "/",
		map[string]string{"User-Agent": "sqlmap/1.7"}); code != 404 {
		t.Errorf("命中 UA 特征应当被拦，实际 %d", code)
	}
	// 命中路径。
	if code, _ := r.caddy.Get("wafish.example.com", "/.git/config", nil); code != 404 {
		t.Errorf("命中路径特征应当被拦，实际 %d", code)
	}
	// **两条特征之间是「或」**：只命中一条也要拦，上面两个各只命中一条。
	// 而正常请求两条都不命中，要放行 —— 没有这一条，
	// 一个把多个条件写成「且」的实现会在上面两个用例上就红，
	// 而一个「无条件拦」的实现只有这一条能抓到。
	if code, _ := r.caddy.Get("wafish.example.com", "/",
		map[string]string{"User-Agent": "Mozilla/5.0"}); code != 200 {
		t.Errorf("两条特征都不命中的正常请求应当放行，实际 %d", code)
	}
}

// TestRequestFilterRejectsBadRegexBeforeDeploy 钉的是**坏正则不能下发**。
//
// 一条写错的正则原样下发到节点上，Caddy 会**拒绝整份配置** ——
// 症状是「所有站点一起下发失败」，而根因是某一条规则里的一个括号。
func TestRequestFilterRejectsBadRegexBeforeDeploy(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "re.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	_, e := r.do("PUT", "/rules/badre", map[string]any{
		"name": "坏正则", "type": "request_filter", "enabled": true,
		"apply_to": []string{"re.example.com"},
		"spec": map[string]any{"filters": []map[string]any{
			{"field": "path", "op": "regex", "value": "(unclosed"},
		}},
	})
	if e.Code != api.CodeValidation {
		t.Fatalf("编译不过的正则应当以 1002 拒绝，实际 code=%d msg=%q", e.Code, e.Msg)
	}
}

// TestEmptyBlacklistIsRejected：空黑名单谁也拦不到，而它在界面上显示为启用。
//
// 与空白名单的理由相反（那个会拦下所有人），所以两句话不同 ——
// **一句「不能为空」对两种类型说的是两件事**。
func TestEmptyBlacklistIsRejected(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "eb.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	_, e := r.do("PUT", "/rules/eb", map[string]any{
		"name": "空黑名单", "type": "ip_blacklist", "enabled": true,
		"apply_to": []string{"eb.example.com"},
		"spec":     map[string]any{"ips": []string{}},
	})
	if e.Code != api.CodeValidation {
		t.Fatalf("空黑名单应当以 1002 拒绝，实际 code=%d msg=%q", e.Code, e.Msg)
	}
}
