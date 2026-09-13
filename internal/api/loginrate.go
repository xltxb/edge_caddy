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

// succeed 把这个来源的失败记录清掉。
//
// **只在没被拦住时才会走到这里**：被拦之后连密码都不会去验，
// 所以攻击者没法靠「在试错之间夹一次正确猜测」把计数清零。
func (l *loginLimiter) succeed(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.fails, k)
	}
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
	l.fails[k] = kept
	return kept
}
