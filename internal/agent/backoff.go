package agent

import "time"

// 退避参数。
//
// steadyAfter 是「这条连接算成功」的线：活满 1 分钟，下一次断开从 1s 重来。
// **复位是承重的**：中间设施会按小时级周期掐长连接（nginx 的
// proxy_read_timeout、CDN 回收），每一次都算「失败」的话，退避只增不减
// ——真实日志里见过 after=1s→2s→4s 跨三次相隔数小时的断开，
// 照那个趋势，几天后每次本来一秒就能恢复的重连都要白等 30 秒。
// 线不能取 0：连上就被踢的循环（证书吊销、版本被拒）会把退避钉死在 1s。
const (
	backoffFloor = time.Second
	backoffCap   = 30 * time.Second
	steadyAfter  = time.Minute
)

// ReconnectBackoff 决定隧道断开后等多久再拨。零值可用。
type ReconnectBackoff struct {
	cur time.Duration
}

// Next 报告这次断开后应当等多久，并推进状态。
// connectedFor 是刚断掉的这条连接活了多久。
func (b *ReconnectBackoff) Next(connectedFor time.Duration) time.Duration {
	if connectedFor >= steadyAfter || b.cur == 0 {
		b.cur = backoffFloor
	}
	wait := b.cur
	if b.cur < backoffCap {
		b.cur *= 2
		if b.cur > backoffCap {
			b.cur = backoffCap
		}
	}
	return wait
}
