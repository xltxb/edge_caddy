package agent

import "context"

// ScrapeTotals 让测试看到心跳里报上去的那两个数，**按它们真正被算出来的路径**。
//
// 走 collect，不在这里重算一遍：重算的话它和 collect 会各自演化，而测试
// 仍然自洽——探针抓到过这件事（第一版这里自己做了减法，于是把 collect 里
// 那行减法改坏，测试照样绿）。
func ScrapeTotals(ctx context.Context, c *CaddyClient, v *VerifyServer) (req, origin uint64) {
	m := newMetricsCollector(c, v)
	out := m.collect(ctx)
	return out.ReqTotal, out.OriginTotal
}
