package agent

import (
	"slices"
	"testing"
)

// TestEdgePortsHandlesIPv6AndBareColon：三种监听写法的端口都要认得出来。
//
// 原先按**第一个**冒号切（`strings.Cut(l, ":")`），于是 `[::1]:443` 得到
// port = `":1]:443"`，ParseUint 失败被静默丢弃（issue #75）。
//
// 症状是连接数恒为 0——而 ports() 读的是**运行中 Caddy 的整份配置**
// （metrics.go），那里的监听地址不由渲染器决定：人手工在节点上配了 v6 监听
// 就会踩到。这与刚修过的那个「Agent 重启后连接数归零」是同一种症状、
// 不同的原因，而两者都表现为「一台正在扛流量的机器报 0」。
func TestEdgePortsHandlesIPv6AndBareColon(t *testing.T) {
	cfg := []byte(`{"apps":{"http":{"servers":{
		"edge":     {"listen":[":80","0.0.0.0:8080"]},
		"edge_tls": {"listen":["[::1]:443","[2001:db8::1]:8443"]}
	}}}}`)

	got := edgePorts(cfg)
	slices.Sort(got)

	want := []uint32{80, 443, 8080, 8443}
	if !slices.Equal(got, want) {
		t.Errorf("解析出 %v，想要 %v —— 丢掉的那些端口上的连接不会被统计，"+
			"而症状是「一台正在扛流量的机器报 0」", got, want)
	}
}

// 不是监听地址的东西不该被当成端口混进来。
func TestEdgePortsIgnoresWhatItCannotParse(t *testing.T) {
	cfg := []byte(`{"apps":{"http":{"servers":{
		"edge": {"listen":["unix//run/caddy.sock","","not-an-addr",":not-a-port"]}
	}}}}`)
	if got := edgePorts(cfg); len(got) != 0 {
		t.Errorf("解析出 %v，一个都不该有", got)
	}
}
