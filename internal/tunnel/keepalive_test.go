package tunnel_test

import (
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/agent"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

// 这两条关系是承重的，各自坏掉的方式都很隐蔽：
//
//  1. Agent 的 ping 间隔低于主控的容忍下限，主控会回 GOAWAY
//     ENHANCE_YOUR_CALM 直接断连——症状是「隧道频繁断开」，
//     而人会去查网络，问题在两个常量的相对大小。
//  2. 两边的 ping 间隔必须都远小于路径上中间设施回收空闲方向的周期
//     （前置 nginx 的 proxy_read_timeout 是 1h，deploy/nginx-console.conf）。
//     keepalive 存在的全部意义就是在那个周期之内制造双向流量；
//     谁把间隔调到一小时以上，等于把这个修复静默撤销。
func TestKeepaliveIntervalsAreCompatible(t *testing.T) {
	if agent.KeepalivePing < tunnel.KeepaliveMinPing {
		t.Fatalf("Agent 的 ping 间隔 %v 低于主控容忍下限 %v——主控会 GOAWAY 断连",
			agent.KeepalivePing, tunnel.KeepaliveMinPing)
	}
	const middleboxIdle = time.Hour // nginx proxy_read_timeout
	if agent.KeepalivePing >= middleboxIdle/2 {
		t.Fatalf("Agent 的 ping 间隔 %v 太接近中间设施的空闲回收周期 %v", agent.KeepalivePing, middleboxIdle)
	}
	if tunnel.KeepaliveServerPing >= middleboxIdle/2 {
		t.Fatalf("主控的 ping 间隔 %v 太接近中间设施的空闲回收周期 %v", tunnel.KeepaliveServerPing, middleboxIdle)
	}
}
