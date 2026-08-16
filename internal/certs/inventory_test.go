package certs_test

import (
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/certs"
)

func report(node string, at time.Time, items ...certs.NodeCert) certs.NodeReport {
	return certs.NodeReport{NodeID: node, At: at, Certs: items}
}

func nc(domain string, notAfter time.Time) certs.NodeCert {
	return certs.NodeCert{Domain: domain, NotAfter: notAfter, Issuer: "Test CA", Serial: "aa"}
}

// 同一域名跨节点聚合时取**最早**的到期时间。
//
// 取最晚会让「有个节点的证书明天就过期」被一个 90 天的副本掩盖，
// 而那个节点明天就会开始拒绝连接。
func TestAggregateTakesEarliestExpiry(t *testing.T) {
	now := time.Now()
	inv := certs.NewInventory(func() time.Time { return now })
	inv.Report(report("node-a", now, nc("api.example.com", now.Add(90*24*time.Hour))))
	inv.Report(report("node-b", now, nc("api.example.com", now.Add(1*24*time.Hour))))

	got := inv.Aggregate()
	if len(got) != 1 {
		t.Fatalf("同一域名应聚合成一条，实际 %d 条", len(got))
	}
	if d := got[0].DaysLeft; d > 1 {
		t.Fatalf("应取最早到期（1 天），实际 %d 天——取最晚会让快过期的那台被掩盖", d)
	}
	if got[0].NodeCount != 2 {
		t.Errorf("覆盖节点数应为 2，实际 %d", got[0].NodeCount)
	}
}

// 按剩余天数升序：最紧急的排最前面。
func TestAggregateSortedByUrgency(t *testing.T) {
	now := time.Now()
	inv := certs.NewInventory(func() time.Time { return now })
	inv.Report(report("node-a", now,
		nc("c.example.com", now.Add(80*24*time.Hour)),
		nc("a.example.com", now.Add(3*24*time.Hour)),
		nc("b.example.com", now.Add(20*24*time.Hour))))

	got := inv.Aggregate()
	want := []string{"a.example.com", "b.example.com", "c.example.com"}
	for i, d := range want {
		if got[i].Domain != d {
			t.Fatalf("应按剩余天数升序，实际 %v", domainsOf(got))
		}
	}
}

// 节点重复上报时**替换**该节点的清单，不追加。
//
// 追加的话，续期换掉的旧证书会一直留在聚合里，而那张旧证书的到期时间更早——
// 于是面板永远显示「即将过期」，直到没人再看它。
func TestReportReplacesPreviousReportFromSameNode(t *testing.T) {
	now := time.Now()
	inv := certs.NewInventory(func() time.Time { return now })
	inv.Report(report("node-a", now, nc("api.example.com", now.Add(3*24*time.Hour))))
	inv.Report(report("node-a", now.Add(time.Minute), nc("api.example.com", now.Add(90*24*time.Hour))))

	got := inv.Aggregate()
	if len(got) != 1 {
		t.Fatalf("应只有一条，实际 %d 条", len(got))
	}
	if got[0].DaysLeft < 80 {
		t.Errorf("续期后应显示新证书，实际还剩 %d 天", got[0].DaysLeft)
	}
}

// 节点离线时，它上报的信息**陈旧程度要能看见**，不假装是最新的。
//
// 一台掉了三天的机器上的证书状态就是三天前的。把它和刚上报的混在一起显示，
// 等于用过时数据做判断——而「过时的证书状态」比没有更危险。
func TestStaleReportsAreVisiblyStale(t *testing.T) {
	now := time.Now()
	inv := certs.NewInventory(func() time.Time { return now })
	inv.Report(report("node-fresh", now, nc("api.example.com", now.Add(60*24*time.Hour))))
	inv.Report(report("node-stale", now.Add(-3*24*time.Hour), nc("api.example.com", now.Add(60*24*time.Hour))))

	got := inv.Aggregate()
	if len(got) != 1 {
		t.Fatalf("应聚合成一条，实际 %d", len(got))
	}
	if !got[0].HasStale {
		t.Fatal("有节点的数据已经陈旧，必须标出来")
	}
	if got[0].OldestReportAge < 2*24*time.Hour {
		t.Errorf("应给出最旧那份数据的年龄，实际 %v", got[0].OldestReportAge)
	}
	// 说清楚是哪几台，否则运维不知道去查哪台机器
	if len(got[0].StaleNodes) != 1 || got[0].StaleNodes[0] != "node-stale" {
		t.Errorf("应指出是哪些节点的数据陈旧，实际 %v", got[0].StaleNodes)
	}
}

// 三档着色的分档由后端给，前端不自己算。
//
// 前端自己算的话，「什么算紧急」这件事就有了两个定义，改一处忘一处。
func TestSeverityBands(t *testing.T) {
	now := time.Now()
	inv := certs.NewInventory(func() time.Time { return now })
	inv.Report(report("node-a", now,
		nc("crit.example.com", now.Add(3*24*time.Hour)),
		nc("warn.example.com", now.Add(20*24*time.Hour)),
		nc("ok.example.com", now.Add(60*24*time.Hour)),
		nc("gone.example.com", now.Add(-time.Hour))))

	bands := map[string]string{}
	for _, c := range inv.Aggregate() {
		bands[c.Domain] = c.Severity
	}
	want := map[string]string{
		"gone.example.com": "crit",
		"crit.example.com": "crit",
		"warn.example.com": "warn",
		"ok.example.com":   "ok",
	}
	for d, w := range want {
		if bands[d] != w {
			t.Errorf("%s 应为 %s 档，实际 %q", d, w, bands[d])
		}
	}
}

// 节点下线后它的上报要能被清掉，否则一台已经拆掉的机器会永远拉低聚合结果。
func TestForgetNodeDropsItsCerts(t *testing.T) {
	now := time.Now()
	inv := certs.NewInventory(func() time.Time { return now })
	inv.Report(report("node-a", now, nc("api.example.com", now.Add(90*24*time.Hour))))
	inv.Report(report("node-b", now, nc("api.example.com", now.Add(1*24*time.Hour))))

	inv.Forget("node-b")
	got := inv.Aggregate()
	if got[0].DaysLeft < 80 {
		t.Errorf("已忘记的节点不该继续拉低到期时间，实际 %d 天", got[0].DaysLeft)
	}
	if got[0].NodeCount != 1 {
		t.Errorf("覆盖节点数应为 1，实际 %d", got[0].NodeCount)
	}
}

// 没有任何上报时返回空列表，不是 nil——前端 v-for 一个 nil 会炸。
func TestEmptyInventoryReturnsEmptySlice(t *testing.T) {
	got := certs.NewInventory(time.Now).Aggregate()
	if got == nil {
		t.Fatal("应返回空切片而不是 nil")
	}
	if len(got) != 0 {
		t.Fatalf("应为空，实际 %d 条", len(got))
	}
}

func domainsOf(cs []certs.Aggregated) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Domain)
	}
	return out
}
