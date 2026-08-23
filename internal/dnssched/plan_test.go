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

func entryOf(t *testing.T, p dnssched.Plan, line, node string) (dnssched.Entry, bool) {
	t.Helper()
	for _, l := range p.Lines {
		if l.Code != line {
			continue
		}
		for _, e := range l.Entries {
			if e.Node == node {
				return e, true
			}
		}
	}
	return dnssched.Entry{}, false
}

// **一个还没配过权重的节点，必须在每条线路上都出现（权重 0）。**
//
// 不这么做会形成一个闭环：节点只在 dns_weights 里有行时才出现在这一页上，
// 而写那张表的唯一入口是 PUT /dns/weights——界面上只能改「已经在列表里的」。
// 于是**新接入的节点永远进不了解析**。
//
// 而这一页看起来完全正常：五条线路齐全，只是每条都空着。前端 agent 在真主控上
// 撞到了它——一台在线、dns_enabled、没下线的节点，五条线路全是 0 个条目。
//
// 这个洞能活到今天，是因为**这个包里每一条测试都从一份「节点已经在里面」的
// 权重表开始**。测试全都假设了产品到不了的那个状态。
func TestNodeWithoutWeightIsStillACandidate(t *testing.T) {
	p := dnssched.Build("cdn.example.com",
		dnssched.Weights{}, // 全新装机：一行权重都没有
		nodes(ok("node-hk-01", "203.0.113.7")))

	for _, line := range []string{"ct", "cu", "cm", "tw", "ov"} {
		e, found := entryOf(t, p, line, "node-hk-01")
		if !found {
			t.Fatalf("线路 %s 上没有 node-hk-01 —— 那它就永远配不上权重，"+
				"因为界面只能改已经在列表里的节点", line)
		}
		if e.Weight != 0 {
			t.Errorf("%s：还没配过的节点权重应当是 0，实际 %d", line, e.Weight)
		}
		if e.InRotation {
			t.Errorf("%s：权重 0 不该在轮换里", line)
		}
		if e.Share != 0 {
			t.Errorf("%s：权重 0 的占比应当是 0，实际 %v", line, e.Share)
		}
	}
}

// 已下线的节点**不进候选**：给一台已经退出的机器配权重是没有意义的动作。
//
// 但**它如果配过权重就仍然出现**——那份配置是人写下的意图，
// 不该因为一次下线就从页面上消失（重新上线之后还要用）。
func TestDrainedNodeIsNotACandidateButKeepsItsConfiguredWeight(t *testing.T) {
	drained := func(id, ip string) dnssched.NodeState {
		n := ok(id, ip)
		n.Drained, n.DNSEnabled = true, false
		return n
	}

	// **夹具里必须有一个没下线的节点。**
	//
	// 这条测试的主张几乎全是否定式（「不该出现」），而否定断言在**候选逻辑
	// 整个不存在**时也会绿——那正是修这个 bug 之前的状态。
	// 加一台在线机器，让这条测试自己带一个肯定断言：候选逻辑没了它就红。
	p := dnssched.Build("cdn.example.com",
		dnssched.Weights{"ct": {"配过的": 60}},
		nodes(drained("配过的", "1.1.1.1"), drained("没配过的", "2.2.2.2"),
			ok("在线的", "3.3.3.3")))

	if _, found := entryOf(t, p, "ct", "在线的"); !found {
		t.Fatal("装置坏了：没下线的节点本该进候选。" +
			"这条测试下面全是否定断言，它们在候选逻辑整个失效时也会绿")
	}

	e, found := entryOf(t, p, "ct", "配过的")
	if !found {
		t.Fatal("配过权重的节点即使下线也该留在页面上：那是人写下的意图")
	}
	if e.Weight != 60 {
		t.Errorf("配置值不该被下线改动，实际 %d", e.Weight)
	}
	if e.InRotation {
		t.Error("下线的节点不该在轮换里")
	}
	if _, found := entryOf(t, p, "ct", "没配过的"); found {
		t.Error("已下线且没配过权重的节点不该进候选 —— 给它配权重是没有意义的动作")
	}
	// 反过来也查一条：这个断言在**候选逻辑整个失效**时也会绿，
	// 所以要有一条肯定断言兜着它。
	if _, found := entryOf(t, p, "cu", "配过的"); found {
		t.Error("cu 上没配过权重、且节点已下线，不该出现")
	}
}
