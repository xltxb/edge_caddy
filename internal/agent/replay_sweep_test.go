package agent

import (
	"fmt"
	"testing"
	"time"
)

// TestAdmitDoesNotScanTheWholeTable：一次判重的代价与表有多大无关。
//
// admit 原先在锁内遍历整张表清过期条目（issue #74）。表的规模是「窗口内的
// 合法签名数」——高 QPS 的受保护域名上，每个请求都是一次 O(n) 的串行扫描，
// 而且握着锁。
//
// 限流桶那边早就是另一条路：清理交给 RunSweeper 定期做，热路径只查插。
//
// **判据是「扫过几条」，不是耗时**：耗时看机器快慢，而扫描量是确定的。
// 计数器只在测试里读（scanned），生产路径上没人看它——与 commitFault /
// RetryBackoff 同一个惯例。
func TestAdmitDoesNotScanTheWholeTable(t *testing.T) {
	c := newReplayCache()
	now := time.Now()
	c.now = func() time.Time { return now }

	// 灌满：窗口内一千条合法签名，这是高 QPS 域名的常态。
	for i := 0; i < 1000; i++ {
		if !c.admit(fmt.Sprintf("sig-%d", i), time.Minute) {
			t.Fatalf("装置：第 %d 条就被判成重放了", i)
		}
	}

	before := c.scanned()
	c.admit("sig-new", time.Minute)
	if n := c.scanned() - before; n > 4 {
		t.Errorf("一次 admit 扫了 %d 条 —— 表里有 1000 条，"+
			"这是每请求一次的 O(n)，而且握着锁", n)
	}
}

// 过期的条目仍然要被清掉，而且仍然算「没见过」。
//
// 少了这条，一个「什么都不清」的实现也能让上面那条绿——而那张表会一直长。
func TestExpiredEntriesAreStillForgotten(t *testing.T) {
	c := newReplayCache()
	now := time.Now()
	c.now = func() time.Time { return now }

	if !c.admit("sig", time.Minute) {
		t.Fatal("第一次应当放行")
	}
	if c.admit("sig", time.Minute) {
		t.Fatal("窗口内重复应当被判成重放")
	}

	now = now.Add(2 * time.Minute) // 过了窗口
	if !c.admit("sig", time.Minute) {
		t.Error("过了窗口的签名应当重新放行 —— 否则它永远用不了第二次")
	}

	// 而那张表不该无限长：清过之后规模要回落。
	c.sweep()
	if got := c.size(); got > 1 {
		t.Errorf("清理之后表里还有 %d 条", got)
	}
}
