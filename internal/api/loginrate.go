package api

import (
	"sync"
	"time"
)

// 登录限速的两个数。
//
// 取 5 次 / 15 分钟：一个人输错五次口令已经该去找密码管理器了，
// 而对暴力破解来说，这把每小时的可试次数从「bcrypt 能跑多少次」
// 压到 20 次——那是一道**有上限**的门，bcrypt 只是让每次变慢。
const (
	loginMaxFails = 5
	loginWindow   = 15 * time.Minute
)

// loginLimiter 数「最近一段时间里，这个来源失败了几次」。
//
// **为什么在内存里**：主控是单进程单写入者（ADR-0011 的同一条前提），
// 进程重启后计数归零是可接受的——重启不是攻击者能随手触发的事，而把它
// 放进库意味着每次登录尝试都写一行，那正好给了攻击者一个写放大的入口。
//
// **为什么按 IP + 用户名两个维度各记一份**：
//   - 只按 IP：攻击者换 IP 就绕过，而一个用户名被全网撞库时没人拦得住
//   - 只按用户名：一个 IP 可以把所有已知用户名各试五次，代价几乎为零；
//     而且攻击者能靠「某个用户名被锁了」反推它存在
//
// 两个维度任一超限就拦。代价是同一个办公室里有人输错五次会连累别人——
// 那是有意的：这道门后面是整个控制面。
type loginLimiter struct {
	mu     sync.Mutex
	fails  map[string][]time.Time
	now    func() time.Time
	maxN   int
	window time.Duration
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		fails:  map[string][]time.Time{},
		now:    time.Now,
		maxN:   loginMaxFails,
		window: loginWindow,
	}
}

// blocked 说明此刻该不该拦下来。它**不计数**——计数只发生在真的失败之后。
func (l *loginLimiter) blocked(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-l.window)
	for _, k := range keys {
		if len(l.recent(k, cutoff)) >= l.maxN {
			return true
		}
	}
	return false
}

// fail 记一次失败。
func (l *loginLimiter) fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-l.window)
	for _, k := range keys {
		l.fails[k] = append(l.recent(k, cutoff), now)
	}
}

// succeed 把**这个用户名**的失败记录清掉。
//
// **不清 IP 维度。** 清了的话，攻击者拿自己的账号夹在中间就能把它中和掉：
// 对 B 试 4 次 → 用自己的账号成功登录一次 → ip 条目被删 → 对 C 再试 4 次……
// 而 IP 维度存在的全部理由就是挡这个（「只按用户名：一个 IP 可以把所有已知
// 用户名各试五次」）。这条路径不受 blocked 的前置判定保护——它走的是成功分支。
//
// 用户名维度该清：那个人证明了自己是他，他自己那几次输错不该继续算数。
// IP 维度靠时间窗自然衰减。
func (l *loginLimiter) succeed(userKey string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, userKey)
}

// size 是表里还有多少个来源。供测试问「它有没有只增不减」。
func (l *loginLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.fails)
}

// recent 丢掉窗口外的那些。调用方已持锁。
//
// 就地裁剪而不是另起定时清理：这张表的规模是「窗口内失败过的来源数」，
// 而每次读写都会顺手把自己那一条裁短——与重放缓存不同，这里的 key 数量
// 不随请求数增长（#74 那条的规模是「窗口内的签名数」，每个请求一条）。
func (l *loginLimiter) recent(k string, cutoff time.Time) []time.Time {
	kept := l.fails[k][:0]
	for _, t := range l.fails[k] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	// **空了就把条目删掉，不是留一个空切片。**
	//
	// `l.fails[k] = kept` 赋值即创建——而 blocked() 对每个 key 都会调这里，
	// 于是每试一个**从未失败过**的用户名就多一个条目，窗口过期后也只是把切片
	// 裁空、条目留着。那是一个未鉴权端点上的无界增长，而下面那句「规模是
	// 窗口内失败过的来源数」正是不做定时清理的全部理由。
	if len(kept) == 0 {
		delete(l.fails, k)
		return nil
	}
	l.fails[k] = kept
	return kept
}
