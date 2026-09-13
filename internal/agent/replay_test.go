package agent

import (
	"testing"
	"time"
)

// **一条签名活多久，由它自己的规则说了算，不由邻居说了算。**
//
// 缓存是所有规则共享的，而清理若按「当前调用的 ttl」一刀切，
// 短窗口规则的每次请求都会把长窗口规则还在窗口内的签名清掉 ——
// 重放保护的实际强度变成取决于同一台节点上别的规则怎么配（issue #27）。
func TestReplayEntryLivesByItsOwnTTL(t *testing.T) {
	c := newReplayCache()
	now := time.Now()
	c.now = func() time.Time { return now }

	if !c.admit("A:sig1", 10*time.Minute) {
		t.Fatal("首次使用应当放行")
	}

	// 两分钟后，邻居规则 B（窗口 1 分钟）来了一个请求。
	now = now.Add(2 * time.Minute)
	c.admit("B:sigX", time.Minute)

	// A 的窗口是 10 分钟，sig1 才用了 2 分钟 —— 重放必须被拒。
	if c.admit("A:sig1", 10*time.Minute) {
		t.Fatal("A 的签名在自己的窗口内被重放成功了 —— 邻居规则的短 ttl 把它清掉了")
	}
}

// 反面：**过了自己的窗口就要被清掉**。
//
// 少了这条，上面那条可以用「永不清理」达成 —— 那会让缓存随流量无限长，
// 而时间戳检查在窗口外本来就会拒绝，留着只是泄漏。
//
// **触发器是 sweep，不再是 admit。** admit 是热路径，它顺手扫全表意味着
// 每个请求一次 O(n) 且握着锁（issue #74）。清理因此交给 RunSweeper——
// 而这条测试要验的「按各自的死期清，不是按当前调用的 ttl 一刀切」
// 没有变，它只是换了一个触发点。
func TestReplayEntryIsEvictedAfterItsOwnTTL(t *testing.T) {
	c := newReplayCache()
	now := time.Now()
	c.now = func() time.Time { return now }

	c.admit("A:sig1", time.Minute)
	now = now.Add(2 * time.Minute)
	c.admit("B:sigX", 10*time.Minute) // B 的窗口 10 分钟，还没到期

	c.sweep()

	if len(c.seen) != 1 {
		t.Fatalf("A 的签名过了自己的窗口还留在缓存里（缓存 %d 条，想要 1：清掉 A、只剩 B）", len(c.seen))
	}
	if _, ok := c.seen["B:sigX"]; !ok {
		t.Error("B 还在自己的窗口内，却被这次清理带走了 —— " +
			"那正是 issue #27 修掉的那个形状（按当前调用的 ttl 一刀切）")
	}
}
