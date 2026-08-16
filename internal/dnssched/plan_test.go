package dnssched_test

import (
	"testing"

	"github.com/xltxb/edge_caddy/internal/dnsprovider"
	"github.com/xltxb/edge_caddy/internal/dnssched"
)

func w(nodeID, ip string, line dnsprovider.Line, weight int, online bool) dnssched.Entry {
	return dnssched.Entry{NodeID: nodeID, IP: ip, Line: line, Weight: weight, Online: online}
}

func byNode(ts []dnsprovider.Target) map[string]dnsprovider.Target {
	m := map[string]dnsprovider.Target{}
	for _, t := range ts {
		m[t.NodeID] = t
	}
	return m
}

// 全部在线时按配置的权重下发。
func TestPlanUsesConfiguredWeights(t *testing.T) {
	got, err := dnssched.Plan([]dnssched.Entry{
		w("node-a", "1.1.1.1", dnsprovider.LineTelecom, 70, true),
		w("node-b", "2.2.2.2", dnsprovider.LineTelecom, 30, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := byNode(got)
	if m["node-a"].Weight != 70 || m["node-b"].Weight != 30 {
		t.Fatalf("权重应原样下发，实际 %+v", got)
	}
}

// **离线节点自动退出解析**，其余节点占比重新归一化。
//
// 不归一化的话，两台各 50 的节点掉一台，剩下那台还是 50——DNSPod 会把它
// 当成唯一目标，结果碰巧是对的；但三台 33/33/34 掉一台时，剩下两台的实际
// 占比就变成了 49/51，与界面上显示的不符。
func TestOfflineNodesAreDroppedAndRemainderRenormalized(t *testing.T) {
	got, err := dnssched.Plan([]dnssched.Entry{
		w("node-a", "1.1.1.1", dnsprovider.LineTelecom, 50, true),
		w("node-b", "2.2.2.2", dnsprovider.LineTelecom, 30, true),
		w("node-c", "3.3.3.3", dnsprovider.LineTelecom, 20, false), // 掉线
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("离线节点应退出解析，实际下发 %d 条", len(got))
	}
	m := byNode(got)
	if _, still := m["node-c"]; still {
		t.Error("离线节点仍在解析里")
	}
	// 50:30 归一化到 100 是 63:37（四舍五入后总和仍为 100）
	total := m["node-a"].Weight + m["node-b"].Weight
	if total != 100 {
		t.Errorf("归一化后总权重应为 100，实际 %d", total)
	}
	if m["node-a"].Weight <= m["node-b"].Weight {
		t.Errorf("原本权重高的仍应更高，实际 a=%d b=%d", m["node-a"].Weight, m["node-b"].Weight)
	}
}

// **全部离线时回落到全部节点**。
//
// 把所有节点都摘掉等于把域名解析成空，比继续解析到一台可能只是心跳抖动的
// 机器更糟：前者是确定的全站不可用，后者只是可能的部分不可用。
func TestAllOfflineFallsBackToEveryone(t *testing.T) {
	got, err := dnssched.Plan([]dnssched.Entry{
		w("node-a", "1.1.1.1", dnsprovider.LineTelecom, 50, false),
		w("node-b", "2.2.2.2", dnsprovider.LineTelecom, 50, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("全部离线应回落到全部节点，实际 %d 条", len(got))
	}
}

// 权重为 0 的节点不参与解析。
//
// 这是运维**主动**摘掉一台的方式，与「掉线」是两回事——它不该被回落逻辑救回来。
func TestZeroWeightNodesAreExcluded(t *testing.T) {
	got, err := dnssched.Plan([]dnssched.Entry{
		w("node-a", "1.1.1.1", dnsprovider.LineTelecom, 100, true),
		w("node-b", "2.2.2.2", dnsprovider.LineTelecom, 0, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].NodeID != "node-a" {
		t.Fatalf("权重 0 的节点不该参与解析，实际 %+v", got)
	}
}

// 权重全为 0 时**不回落**——那是运维明确表达的意图。
//
// 与「全部掉线」不同：掉线是意外，权重清零是有人一个个敲进去的。
// 把它救回来等于推翻一个明确的决定。
func TestAllZeroWeightsIsAnErrorNotAFallback(t *testing.T) {
	_, err := dnssched.Plan([]dnssched.Entry{
		w("node-a", "1.1.1.1", dnsprovider.LineTelecom, 0, true),
		w("node-b", "2.2.2.2", dnsprovider.LineTelecom, 0, true),
	})
	if err == nil {
		t.Fatal("权重全为 0 会把解析清空，应报错而不是悄悄回落")
	}
}

// 每条线路各自归一化，互不影响。
//
// 混在一起算的话，电信有 5 台、境外只有 1 台时，境外那台的权重会被稀释到
// 几乎收不到流量——而它本该独占境外线路的全部流量。
func TestLinesAreNormalizedIndependently(t *testing.T) {
	got, err := dnssched.Plan([]dnssched.Entry{
		w("node-a", "1.1.1.1", dnsprovider.LineTelecom, 50, true),
		w("node-b", "2.2.2.2", dnsprovider.LineTelecom, 50, true),
		w("node-c", "3.3.3.3", dnsprovider.LineOverseas, 10, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := byNode(got)
	if m["node-c"].Weight != 100 {
		t.Errorf("境外线路只有一台，应独占 100，实际 %d", m["node-c"].Weight)
	}
	if m["node-a"].Weight+m["node-b"].Weight != 100 {
		t.Errorf("电信线路两台应合计 100，实际 %d+%d",
			m["node-a"].Weight, m["node-b"].Weight)
	}
}

// 某条线路全部离线时，只回落这条线路，不影响别的线路。
func TestFallbackIsPerLine(t *testing.T) {
	got, err := dnssched.Plan([]dnssched.Entry{
		w("node-a", "1.1.1.1", dnsprovider.LineTelecom, 100, true),
		w("node-b", "2.2.2.2", dnsprovider.LineOverseas, 100, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := byNode(got)
	if _, ok := m["node-b"]; !ok {
		t.Error("境外线路全部离线，应回落到该线路的全部节点")
	}
	if m["node-a"].Weight != 100 {
		t.Errorf("电信线路不该被影响，实际 %d", m["node-a"].Weight)
	}
}

// 归一化的舍入不能把总和弄丢。
//
// 三台等权重时，100/3 = 33.33 各自取整是 99——差的那 1 会让最后一台
// 少收千分之三的流量。数值不大，但「界面上写 33.3% 实际 33.0%」这种
// 对不上的东西查起来极其费时。
func TestRoundingKeepsTotalAtHundred(t *testing.T) {
	for _, n := range []int{3, 6, 7, 9, 11} {
		var in []dnssched.Entry
		for i := 0; i < n; i++ {
			in = append(in, w(string(rune('a'+i)), "1.1.1."+string(rune('0'+i)),
				dnsprovider.LineTelecom, 10, true))
		}
		got, err := dnssched.Plan(in)
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for _, tg := range got {
			total += tg.Weight
		}
		if total != 100 {
			t.Errorf("%d 台等权重时总和应为 100，实际 %d", n, total)
		}
	}
}

// 权重下限是 1：归一化后不能把一台本该参与解析的节点算成 0。
//
// 算成 0 等于把它摘出解析，而运维给的是一个正数——那是「少给点流量」，
// 不是「不要流量」。
func TestTinyShareStillGetsAtLeastOne(t *testing.T) {
	var in []dnssched.Entry
	in = append(in, w("big", "1.1.1.1", dnsprovider.LineTelecom, 10000, true))
	in = append(in, w("tiny", "2.2.2.2", dnsprovider.LineTelecom, 1, true))

	got, err := dnssched.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	m := byNode(got)
	if m["tiny"].Weight < 1 {
		t.Errorf("占比极小的节点也该至少拿到 1，实际 %d", m["tiny"].Weight)
	}
}

// 没有 IP 的节点不能进解析计划。
//
// 空 IP 会被服务商拒绝，而报错是「记录值非法」——排查时要先想到是节点信息
// 不全，那一步很难想到。
func TestNodesWithoutIPAreRejected(t *testing.T) {
	_, err := dnssched.Plan([]dnssched.Entry{
		w("node-a", "", dnsprovider.LineTelecom, 100, true),
	})
	if err == nil {
		t.Fatal("没有 IP 的节点不该进解析计划")
	}
}

// 空输入报错，不返回空计划：空计划下发下去会把解析清空。
func TestEmptyInputIsAnError(t *testing.T) {
	if _, err := dnssched.Plan(nil); err == nil {
		t.Fatal("没有任何节点时应报错，而不是返回一个会把解析清空的空计划")
	}
}
