package api

import (
	"strconv"
	"testing"
	"time"
)

// TestSuccessDoesNotClearTheIPCounter：一次成功登录不该把这个 IP 的失败记录清零。
//
// `succeed(ipKey, userKey)` 原先对两个维度都 delete。而 `store.CreateUser`
// 允许多个账号，于是攻击者可以拿**自己的**账号把 IP 维度中和掉：
//
//	对 B 试 4 次 → 用自己的账号 A 成功登录一次 → ip: 条目被删 → 对 C 再试 4 次 …
//
// IP 维度存在的全部理由就是挡这个（「只按用户名：一个 IP 可以把所有已知
// 用户名各试五次」）。而这条路径不受 blocked 的前置判定保护——它走的是
// 成功分支。
//
// 用户名维度该清：那个人证明了自己是他，他自己那几次输错不该继续算数。
func TestSuccessDoesNotClearTheIPCounter(t *testing.T) {
	l := newLoginLimiter()
	const ip = "ip:203.0.113.7"

	for i := 0; i < loginMaxFails-1; i++ {
		l.fail(ip, "user:victim")
	}
	// 攻击者用自己的账号成功登录一次。
	l.succeed("user:attacker")

	// 同一个 IP 再试一次就该到上限。
	l.fail(ip, "user:victim2")
	if !l.blocked(ip) {
		t.Error("一次成功登录把 IP 维度清零了 —— 攻击者拿自己的账号夹在中间，" +
			"就能把所有已知用户名各试满五次，而 IP 维度存在的理由正是挡这个")
	}
	// 而那个人自己的用户名维度该被清掉。
	if l.blocked("user:attacker") {
		t.Error("登录成功的人自己的用户名维度不该还锁着")
	}
}

// TestLimiterDoesNotGrowOnEveryAttempt：没失败过的来源不该在表里占一个条目。
//
// `blocked()` 对每个 key 调 `recent()`，而 `recent` 末尾的 `l.fails[k] = kept`
// **对一个从未失败过的 key 也会建条目**（赋值即创建），窗口过期后也只是把
// 切片裁空、条目留着。唯一的删除路径是 succeed。
//
// 这是一个**未鉴权端点**上的无界内存增长：每试一个新用户名就多两个条目。
// 而 loginLimiter 的注释写着「这张表的规模是窗口内失败过的来源数」——
// 那句话正是不做清理的全部理由。
func TestLimiterDoesNotGrowOnEveryAttempt(t *testing.T) {
	l := newLoginLimiter()
	for i := 0; i < 1000; i++ {
		l.blocked("ip:203.0.113.7", "user:"+strconv.Itoa(i))
	}
	if n := l.size(); n != 0 {
		t.Errorf("一次失败都没有，表里却有 %d 条 —— "+
			"这是未鉴权端点上的无界增长，而注释说规模是「失败过的来源数」", n)
	}
}

// 过了窗口的条目要真的消失，而不是留一个空切片。
func TestExpiredEntriesLeaveTheTable(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()
	l.now = func() time.Time { return now }

	l.fail("ip:203.0.113.7", "user:abiu")
	if l.size() != 2 {
		t.Fatalf("装置：失败一次应当留下两个条目，实际 %d", l.size())
	}

	now = now.Add(loginWindow + time.Minute)
	l.blocked("ip:203.0.113.7", "user:abiu") // 一次读就该把过期的裁掉

	if n := l.size(); n != 0 {
		t.Errorf("过了窗口还剩 %d 条 —— 裁空了切片却留着条目，表只增不减", n)
	}
}
