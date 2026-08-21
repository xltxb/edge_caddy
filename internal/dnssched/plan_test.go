package dnssched_test

import (
	"testing"

	"github.com/xltxb/edge_caddy/internal/dnssched"
)

func nodes(specs ...dnssched.NodeState) []dnssched.NodeState { return specs }

func ok(id, ip string) dnssched.NodeState {
	return dnssched.NodeState{ID: id, IP: ip, DNSEnabled: true, Status: "ok"}
}

func shareOf(t *testing.T, p dnssched.Plan, line, node string) float64 {
	t.Helper()
	for _, l := range p.Lines {
		if l.Code != line {
			continue
		}
		for _, e := range l.Entries {
			if e.Node == node {
				return e.Share
			}
		}
	}
	t.Fatalf("线路 %s 上没有 %s", line, node)
	return 0
}

func TestSharesNormalizeToHundred(t *testing.T) {
	p := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"a": 60, "b": 40}},
		nodes(ok("a", "1.1.1.1"), ok("b", "2.2.2.2")))

	if got := shareOf(t, p, "ct", "a"); got != 60 {
		t.Errorf("a 的占比 = %v，想要 60", got)
	}
	if got := shareOf(t, p, "ct", "b"); got != 40 {
		t.Errorf("b 的占比 = %v，想要 40", got)
	}
}

// 被摘除的节点占比为 0，权重在**其余节点之间**重新归一化。
//
// Weight 是配置值、Share 是实际占比：被摘的那个 Weight 仍是配置的数字
// （人没改过它），Share 才是 0。两者合成一个字段就说不清「为什么它是 0」。
func TestDisabledNodeDropsOutAndOthersRenormalize(t *testing.T) {
	down := ok("b", "2.2.2.2")
	down.DNSEnabled = false

	p := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"a": 60, "b": 40}},
		nodes(ok("a", "1.1.1.1"), down))

	if got := shareOf(t, p, "ct", "a"); got != 100 {
		t.Errorf("剩下的节点应当拿到 100%%，实际 %v", got)
	}
	if got := shareOf(t, p, "ct", "b"); got != 0 {
		t.Errorf("被摘除的节点占比应当是 0，实际 %v", got)
	}
	for _, l := range p.Lines {
		if l.Code != "ct" {
			continue
		}
		for _, e := range l.Entries {
			if e.Node == "b" && e.Weight != 40 {
				t.Errorf("被摘除的节点 Weight 应当保持配置值 40，实际 %d", e.Weight)
			}
		}
	}
}

// status=down 的节点即使 dns_enabled 还是 true 也不参与解析。
//
// 这个组合真实存在：刚判定离线、而摘除那一步失败了。
// 两个条件缺一不可，否则流量会继续往一台死机器上打。
func TestDownNodeIsExcludedEvenIfDNSStillEnabled(t *testing.T) {
	dead := ok("b", "2.2.2.2")
	dead.Status = "down" // dns_enabled 仍是 true

	p := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"a": 50, "b": 50}},
		nodes(ok("a", "1.1.1.1"), dead))

	if got := shareOf(t, p, "ct", "b"); got != 0 {
		t.Fatalf("已判定离线的节点不该分到流量，实际 %v", got)
	}
	if got := shareOf(t, p, "ct", "a"); got != 100 {
		t.Fatalf("a 应当拿到全部，实际 %v", got)
	}
}

// warn 的节点**仍然参与解析**：它是「连着但不健康」。
// 自动摘掉会把负载全压到其余节点上，很可能连锁。要摘由人决定。
func TestWarnNodeStaysInRotation(t *testing.T) {
	hot := ok("b", "2.2.2.2")
	hot.Status = "warn"

	p := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"a": 50, "b": 50}},
		nodes(ok("a", "1.1.1.1"), hot))

	if got := shareOf(t, p, "ct", "b"); got != 50 {
		t.Fatalf("warn 的节点应当继续参与解析，实际占比 %v", got)
	}
}

// **整条线路的节点全部离线**——一次机房故障就够了。
// 除零或 NaN 会让这个页面在最需要看的时候崩掉。
func TestAllNodesOfflineProducesZeroSharesNotNaN(t *testing.T) {
	a, b := ok("a", "1.1.1.1"), ok("b", "2.2.2.2")
	a.Status, b.Status = "down", "down"

	p := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"a": 60, "b": 40}},
		nodes(a, b))

	for _, node := range []string{"a", "b"} {
		got := shareOf(t, p, "ct", node)
		if got != 0 {
			t.Errorf("%s 的占比 = %v，想要 0", node, got)
		}
		if got != got { // NaN != NaN
			t.Errorf("%s 的占比是 NaN", node)
		}
	}
	if r := p.Rotation("ct"); len(r) != 0 {
		t.Errorf("全部离线时轮换应当为空，实际 %+v", r)
	}
}

// 权重为 0 等于「配置上就不参与」，不该被算进分母。
func TestZeroWeightIsNotInRotation(t *testing.T) {
	p := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"a": 100, "b": 0}},
		nodes(ok("a", "1.1.1.1"), ok("b", "2.2.2.2")))

	if got := shareOf(t, p, "ct", "a"); got != 100 {
		t.Errorf("a = %v，想要 100", got)
	}
	if r := p.Rotation("ct"); len(r) != 1 || r[0].Node != "a" {
		t.Errorf("轮换里应当只有 a，实际 %+v", r)
	}
}

// 配了权重但节点已经不存在了——删节点时留下的孤儿行。
// 不能因此把整条线路算崩。
func TestUnknownNodeIsIgnoredNotCrashed(t *testing.T) {
	p := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"a": 50, "ghost": 50}},
		nodes(ok("a", "1.1.1.1")))

	if got := shareOf(t, p, "ct", "a"); got != 100 {
		t.Fatalf("a 应当拿到全部，实际 %v", got)
	}
	if got := shareOf(t, p, "ct", "ghost"); got != 0 {
		t.Fatalf("不存在的节点占比应当是 0，实际 %v", got)
	}
}

// 五条线路始终齐全，即使某条一个节点都没配 —— 前端按线路分组渲染，
// 缺一条会让那一组凭空消失，而不是显示成「这条线还没配」。
func TestAllFiveLinesAlwaysPresent(t *testing.T) {
	p := dnssched.Build("cdn.example.com", dnssched.Weights{"ct": {"a": 1}}, nodes(ok("a", "1.1.1.1")))
	if len(p.Lines) != 5 {
		t.Fatalf("线路数 = %d，想要 5", len(p.Lines))
	}
	want := []string{"ct", "cu", "cm", "tw", "ov"}
	for i, w := range want {
		if p.Lines[i].Code != w {
			t.Errorf("第 %d 条线路 = %s，想要 %s", i, p.Lines[i].Code, w)
		}
	}
}
