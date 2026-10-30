package health

import (
	"testing"
	"time"
)

// TestHelloCountsAsAliveForOfflineJudgement：隧道刚接上的节点不会在第一个心跳到达前被判 down。
//
// 灰度上真实发生过：Cloudflare 掐断 node-hk-02 的隧道，重连走完时
// 离线计数已经攒到差一次，主控收到 Hello 1.3 秒后照样判了 down、摘了解析，
// 3 秒后第一个心跳到了又恢复。一次合法的 mTLS Hello 本身就是存活的证据。
func TestHelloCountsAsAliveForOfflineJudgement(t *testing.T) {
	const interval, threshold = time.Second, 3
	const id = "node-hk-02"

	m := New(Config{Interval: interval, Threshold: threshold})
	stale := time.Now().Add(-10 * interval)
	m.mu.Lock()
	m.nodes[id] = &nodeState{seen: true, misses: threshold - 1, last: Sample{At: stale}}
	m.mu.Unlock()

	m.Alive(id)
	m.tick(t.Context())

	m.mu.Lock()
	st := m.nodes[id]
	down, misses, at := st.downSent, st.misses, st.last.At
	m.mu.Unlock()

	if down {
		t.Fatal("收到 Hello 之后的那次 tick 不该判 down")
	}
	if misses != 0 {
		t.Fatalf("misses = %d，Hello 应当把它清零", misses)
	}
	// **样本的时间不能被 Hello 刷新。** 采样拿 last.At 判样本新不新鲜
	// （StaleAfter），Hello 不带任何观测量——刷了它，一份旧数字就被当成了当下。
	if !at.Equal(stale) {
		t.Fatalf("last.At 被改成了 %v，Hello 不该动样本", at)
	}
}

// TestAliveDoesNotTrackUnknownNodes：Alive 不为没见过的节点建条目。
//
// 建了的话，seen 为假、last 为空，Latest 会说「没有样本」而 tick 会跳过它——
// 无害但无用。节点该从哪里进 map，还是 Observe 与 warm 说了算。
func TestAliveDoesNotTrackUnknownNodes(t *testing.T) {
	m := New(Config{Interval: time.Second, Threshold: 3})
	m.Alive("node-new")
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes["node-new"]; ok {
		t.Fatal("Alive 不该为没见过的节点建条目")
	}
}
