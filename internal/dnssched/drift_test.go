package dnssched_test

import (
	"testing"

	"github.com/xltxb/edge_caddy/internal/dnsprovider"
	"github.com/xltxb/edge_caddy/internal/dnssched"
)

func target(node, ip string, line dnsprovider.Line, weight int) dnsprovider.Target {
	return dnsprovider.Target{NodeID: node, IP: ip, Line: line, Weight: weight}
}

func liveRec(id, ip string, line dnsprovider.Line, weight int) dnsprovider.ARecord {
	return dnsprovider.ARecord{ID: id, Sub: "@", Value: ip, Line: line, Weight: weight}
}

// 一致时没有漂移。
func TestNoDriftWhenLiveMatchesPlan(t *testing.T) {
	d := dnssched.Diff(
		[]dnsprovider.Target{
			target("node-a", "1.1.1.1", dnsprovider.LineTelecom, 60),
			target("node-b", "2.2.2.2", dnsprovider.LineTelecom, 40),
		},
		[]dnsprovider.ARecord{
			liveRec("1", "1.1.1.1", dnsprovider.LineTelecom, 60),
			liveRec("2", "2.2.2.2", dnsprovider.LineTelecom, 40),
		},
	)
	if d.Drifted() {
		t.Fatalf("完全一致时不该有漂移：%+v", d)
	}
}

// **权重不同**是最要紧的一类漂移：改了权重却没生效，不看线上就无从察觉。
func TestWeightMismatchIsDrift(t *testing.T) {
	d := dnssched.Diff(
		[]dnsprovider.Target{target("node-a", "1.1.1.1", dnsprovider.LineTelecom, 60)},
		[]dnsprovider.ARecord{liveRec("1", "1.1.1.1", dnsprovider.LineTelecom, 30)},
	)
	if !d.Drifted() {
		t.Fatal("权重不同应判为漂移")
	}
	if len(d.WeightChanged) != 1 {
		t.Fatalf("应报告一处权重差异，实际 %+v", d)
	}
	got := d.WeightChanged[0]
	if got.Want != 60 || got.Live != 30 {
		t.Errorf("要说清楚「库里多少、线上多少」，实际 want=%d live=%d", got.Want, got.Live)
	}
}

// 线上多出来的记录也是漂移：可能是别人在服务商控制台手动加的，
// 而它正在分走流量。
func TestExtraLiveRecordIsDrift(t *testing.T) {
	d := dnssched.Diff(
		[]dnsprovider.Target{target("node-a", "1.1.1.1", dnsprovider.LineTelecom, 100)},
		[]dnsprovider.ARecord{
			liveRec("1", "1.1.1.1", dnsprovider.LineTelecom, 100),
			liveRec("2", "9.9.9.9", dnsprovider.LineTelecom, 100),
		},
	)
	if !d.Drifted() {
		t.Fatal("线上多出记录应判为漂移——它正在分走流量")
	}
	if len(d.OnlyLive) != 1 || d.OnlyLive[0].Value != "9.9.9.9" {
		t.Errorf("应报告线上多出的那条，实际 %+v", d.OnlyLive)
	}
}

// 计划里有、线上没有：改了权重还没保存，或者保存失败了。
func TestMissingLiveRecordIsDrift(t *testing.T) {
	d := dnssched.Diff(
		[]dnsprovider.Target{
			target("node-a", "1.1.1.1", dnsprovider.LineTelecom, 50),
			target("node-b", "2.2.2.2", dnsprovider.LineTelecom, 50),
		},
		[]dnsprovider.ARecord{liveRec("1", "1.1.1.1", dnsprovider.LineTelecom, 50)},
	)
	if !d.Drifted() {
		t.Fatal("计划里有而线上没有，应判为漂移")
	}
	if len(d.OnlyPlanned) != 1 || d.OnlyPlanned[0].IP != "2.2.2.2" {
		t.Errorf("应报告缺的那条，实际 %+v", d.OnlyPlanned)
	}
}

// 同一 IP 在不同线路上是**不同的记录**，不能混为一谈。
//
// 混了的话，「电信 60 / 境外 40」会被看成同一条 IP 的两个权重，
// 判成漂移或判成一致都不对。
func TestSameIPOnDifferentLinesAreDistinct(t *testing.T) {
	d := dnssched.Diff(
		[]dnsprovider.Target{
			target("node-a", "1.1.1.1", dnsprovider.LineTelecom, 60),
			target("node-a", "1.1.1.1", dnsprovider.LineOverseas, 40),
		},
		[]dnsprovider.ARecord{
			liveRec("1", "1.1.1.1", dnsprovider.LineTelecom, 60),
			liveRec("2", "1.1.1.1", dnsprovider.LineOverseas, 40),
		},
	)
	if d.Drifted() {
		t.Fatalf("同 IP 不同线路应各自比对，实际报了漂移：%+v", d)
	}
}

// 线上什么都没有时是漂移，而不是「一致」。
//
// 空对空返回一致的话，一个从没保存过的域名会显示「已同步」——
// 而它实际上根本没有解析。
func TestEmptyLiveAgainstNonEmptyPlanIsDrift(t *testing.T) {
	d := dnssched.Diff(
		[]dnsprovider.Target{target("node-a", "1.1.1.1", dnsprovider.LineTelecom, 100)},
		nil,
	)
	if !d.Drifted() {
		t.Fatal("线上没有任何记录时应判为漂移")
	}
}

// 摘要要能直接显示给人看，不用前端再拼一遍。
func TestSummaryIsHumanReadable(t *testing.T) {
	d := dnssched.Diff(
		[]dnsprovider.Target{target("node-a", "1.1.1.1", dnsprovider.LineTelecom, 60)},
		[]dnsprovider.ARecord{liveRec("1", "1.1.1.1", dnsprovider.LineTelecom, 30)},
	)
	s := d.Summary()
	if s == "" {
		t.Fatal("有漂移时摘要不该为空")
	}
	for _, want := range []string{"1.1.1.1", "60", "30"} {
		if !containsStr(s, want) {
			t.Errorf("摘要里应含 %q，实际 %q", want, s)
		}
	}

	clean := dnssched.Diff(
		[]dnsprovider.Target{target("node-a", "1.1.1.1", dnsprovider.LineTelecom, 60)},
		[]dnsprovider.ARecord{liveRec("1", "1.1.1.1", dnsprovider.LineTelecom, 60)},
	)
	if clean.Summary() != "" {
		t.Errorf("没有漂移时摘要应为空，实际 %q", clean.Summary())
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
