package caddytest

import (
	"net/http"
	"testing"
)

// TestTestDoubleDoesNotReuseConnectionsEither 守的是**替身与被测对象的一致**。
//
// internal/agent 那边有 TestCaddyClientDoesNotReuseConnections 钉住
// NewCaddyClient 不复用连接。它管不到这里：unixClient 是这个包自己造的，
// 把它的 DisableKeepAlives 删掉，那条测试照样全绿。
//
// **替身一旦和被测对象走了不同的路径，测试测的就是另一个东西。**
// 更糟的是它会变成假保障：那个 EOF 竞态只在复用连接时出现，
// 替身不复用就永远撞不上——于是「测试跑过了」不代表线上那条路跑得过。
//
// 这条是白盒的，理由与 agent 那条相同：竞态复现不出来，
// 只能钉住那个消除它的决定。
func TestTestDoubleDoesNotReuseConnectionsEither(t *testing.T) {
	c := unixClient("/tmp/nonexistent.sock")
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 不是 *http.Transport（%T），无从保证它不复用连接", c.Transport)
	}
	if !tr.DisableKeepAlives {
		t.Error("替身打开了连接复用，而 agent.NewCaddyClient 没有。" +
			"两边不一致时，测试跑的是线上不会走的那条路径")
	}
}
