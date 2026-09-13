package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
)

// fakeStream 是一条能被测试掐断的隧道。
//
// Recv 一直挡着，直到 breakIt 被调用——那一刻它回一个错误，与真隧道断开时
// 一模一样。Send 只数数：这条测试关心的是**还有没有人在发**，不是发了什么。
type fakeStream struct {
	broken chan struct{}
	once   sync.Once

	mu    sync.Mutex
	sends int
}

func newFakeStream() *fakeStream { return &fakeStream{broken: make(chan struct{})} }

func (f *fakeStream) Send(*edgev1.AgentMsg) error {
	f.mu.Lock()
	f.sends++
	f.mu.Unlock()
	return nil
}

func (f *fakeStream) Recv() (*edgev1.MasterMsg, error) {
	<-f.broken
	return nil, errors.New("隧道断开")
}

func (f *fakeStream) breakIt() { f.once.Do(func() { close(f.broken) }) }

func (f *fakeStream) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends
}

// TestServeStopsItsLoopsWhenTheTunnelDrops：隧道断开之后，这次连接起的后台
// 循环必须停下来。
//
// 它们原先只在**调用方那个 ctx** 结束时退出，而调用方传的是进程级的信号 ctx
// （cmd/agent/main.go 的重连循环）——隧道断开不会取消它。于是 Run 返回、
// main 重连、再起一份，而上一份还在那儿转（issue #42）。
//
// dial.go:29 记着真实日志：中间设施按小时掐长连接，一天断三次。断 N 次就是
// N 份心跳循环，每份每 3 秒做一次 200ms 的 CPU 采样并列一遍全机 TCP 连接。
//
// **判据是「serve 返回之后流上还有没有新的发送」**，不是 goroutine 计数：
// 计数要配一个容差，而容差正好盖住「泄漏了一两条」这种最常见的情形。
func TestServeStopsItsLoopsWhenTheTunnelDrops(t *testing.T) {
	f := newFakeStream()
	a := New(Config{
		NodeID:     "node-hk-01",
		CaddyAdmin: "http://127.0.0.1:1",
		Heartbeat:  5 * time.Millisecond,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	// 进程级的 ctx：它在整个测试期间都不会结束，正如线上那条。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.serve(ctx, f, newTunnelWriter(f))
	}()

	// 等心跳真的跑起来，否则下面那个「不再增长」会因为它还没开始而假绿。
	deadline := time.Now().Add(2 * time.Second)
	for f.sendCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.sendCount() == 0 {
		t.Fatal("心跳一条都没发出来——装置没跑起来，后面的断言是空的")
	}

	f.breakIt()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("隧道断了，serve 没有返回")
	}

	// serve 已经返回。给还活着的循环足够多个周期去暴露自己。
	settled := f.sendCount()
	time.Sleep(60 * time.Millisecond) // 12 个心跳周期
	if got := f.sendCount(); got != settled {
		t.Errorf("serve 返回后流上又多了 %d 条消息——这次连接的循环没有停，"+
			"而 main 已经在重连并再起一份了", got-settled)
	}
}

// scriptedStream 先按剧本递出几条主控消息，然后一直挡着（直到被掐断）。
type scriptedStream struct {
	script chan *edgev1.MasterMsg
	broken chan struct{}
	once   sync.Once

	mu   sync.Mutex
	sent []*edgev1.AgentMsg
}

func newScriptedStream(msgs ...*edgev1.MasterMsg) *scriptedStream {
	s := &scriptedStream{
		script: make(chan *edgev1.MasterMsg, len(msgs)),
		broken: make(chan struct{}),
	}
	for _, m := range msgs {
		s.script <- m
	}
	return s
}

func (s *scriptedStream) Send(m *edgev1.AgentMsg) error {
	s.mu.Lock()
	s.sent = append(s.sent, m)
	s.mu.Unlock()
	return nil
}

func (s *scriptedStream) Recv() (*edgev1.MasterMsg, error) {
	select {
	case m := <-s.script:
		return m, nil
	case <-s.broken:
		return nil, errors.New("隧道断开")
	}
}

func (s *scriptedStream) breakIt() { s.once.Do(func() { close(s.broken) }) }

// seen 回报已经发出去的消息里，各类型分别有几条。
func (s *scriptedStream) seen() (probes, pushes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.sent {
		switch m.M.(type) {
		case *edgev1.AgentMsg_ProbeResult:
			probes++
		case *edgev1.AgentMsg_PushResult:
			pushes++
		}
	}
	return probes, pushes
}

// TestProbeIsAnsweredWhileADeployIsStillRunning：一次慢下发不该让探活失联。
//
// 下发原先在读循环里**同步**跑，上限是主控给的 DeadlineMs（5 秒）。那段时间里
// 这条隧道读不到任何东西——主控推不下来配置，也探不了活，于是一台**正在正常
// 下发**的机器被判成不可达（issue #61）。
//
// 紧邻的 Drain 分支早就因为完全相同的理由改成了 `go`，注释写着：「排空要等，
// 不能占着这条读循环——占住的话主控这段时间推不下来配置也探不了活」。
// **同一条理由应当得到同一种处置。**
func TestProbeIsAnsweredWhileADeployIsStillRunning(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// 探活走 GET，它必须一直答得动。
			_, _ = w.Write([]byte(`{"apps":{}}`))
			return
		}
		// 下发走写请求：卡在这里，模拟一次慢重载。
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer admin.Close()
	defer releaseOnce.Do(func() { close(release) })

	f := newScriptedStream(
		&edgev1.MasterMsg{M: &edgev1.MasterMsg_Push{Push: &edgev1.PushConfig{
			CfgVersion: "cfg-1", CaddyJson: []byte(`{"apps":{"http":{"servers":{}}}}`), DeadlineMs: 5000,
		}}},
		&edgev1.MasterMsg{M: &edgev1.MasterMsg_Probe{Probe: &edgev1.Probe{Id: "p1"}}},
	)

	a := New(Config{
		NodeID:     "node-hk-01",
		CaddyAdmin: admin.URL,
		Heartbeat:  time.Hour, // 心跳发一次就够，别把断言搅浑
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.serve(ctx, f, newTunnelWriter(f)) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if probes, _ := f.seen(); probes > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	probes, pushes := f.seen()
	if probes == 0 {
		t.Fatal("下发还没做完，探活就一直没人回 —— " +
			"主控这段时间会把一台正在正常下发的机器判成不可达")
	}
	if pushes != 0 {
		t.Fatalf("装置坏了：下发本该还卡着，却已经回了 %d 条结果", pushes)
	}

	releaseOnce.Do(func() { close(release) })
	f.breakIt()
}

// pushStuckAt 起一个卡在「应用配置」那一步上的 Agent，并把放行的开关交出去。
//
// 两个测试共用：它们看的是同一个瞬间（下发写到一半、隧道断了）的两件不同的事。
func pushStuckAt(t *testing.T) (a *Agent, f *scriptedStream, release func(), done chan struct{}) {
	t.Helper()
	gate := make(chan struct{})
	release = sync.OnceFunc(func() { close(gate) })
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"apps":{}}`))
			return
		}
		<-gate
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(admin.Close)
	// **无论测试怎么结束都要放行。** 断言失败时 t.Fatal 直接退出，那个 handler
	// 会永远卡在 <-gate 上，而 admin.Close 要等它——于是一次本该是「红」的
	// 结果变成整个包挂到超时 panic，谁也看不出是哪条断言没过。
	// Cleanup 是后进先出，所以这句排在 admin.Close 之前跑。
	t.Cleanup(release)

	f = newScriptedStream(&edgev1.MasterMsg{M: &edgev1.MasterMsg_Push{Push: &edgev1.PushConfig{
		CfgVersion: "cfg-1", CaddyJson: []byte(`{"apps":{"http":{"servers":{}}}}`), DeadlineMs: 5000,
	}}})
	a = New(Config{
		NodeID: "node-hk-01", CaddyAdmin: admin.URL, Heartbeat: time.Hour,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done = make(chan struct{})
	go func() { defer close(done); _ = a.serve(ctx, f, newTunnelWriter(f)) }()

	time.Sleep(50 * time.Millisecond) // 等下发卡在 ApplyConfig 上
	return a, f, release, done
}

// TestApplyFinishesEvenIfTheTunnelDrops：下发写到一半时断连，配置仍要落地。
//
// handlePush 改成 `go` 起之后（issue #61）它吃的是 connCtx，而 applyCtx 派生
// 自它。于是隧道一断，ApplyConfig 被中途取消——**而它前面两步的副作用已经
// 生效且没有回滚**：
//
//	a.verify.SetRules(rules)           // 校验规则已经换成新的
//	writeUpstreamCert(p)               // 回源证书已经落盘
//	a.caddy.ApplyConfig(applyCtx, …)   // 被取消
//
// 节点于是拿**新规则配旧路由**跑，而 a.cfgVersion 仍是旧值——重连后心跳报的
// 也是旧版本，主控看到的是「这台没跟上」而不是「这台状态不自洽」。真正在跑
// 的那套东西没有任何一处记着。
//
// 断言落在 a.cfgVersion 上：它只在 ApplyConfig 成功之后才被写，是「这份配置
// 真的应用了」在 Agent 内部唯一的记号。这里轮询而不是等 serve 返回，
// 是为了让这条断言与 TestServeWaitsForThePushItStarted 各守各的。
func TestApplyFinishesEvenIfTheTunnelDrops(t *testing.T) {
	a, f, release, _ := pushStuckAt(t)

	f.breakIt()                       // 隧道断了
	time.Sleep(50 * time.Millisecond) // 让 connCtx 的取消传到位
	release()                         // 放行那次 POST

	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		got := a.cfgVersion
		a.mu.Unlock()
		if got == "cfg-1" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cfg_version = %q，想要 cfg-1 —— 断连把下发掐死在中间了："+
				"校验规则和回源证书已经换成新的，Caddy 上跑的还是旧配置", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestServeWaitsForThePushItStarted：serve 返回时，它 go 起的下发也结束了。
//
// serve 的 wg 原先只数了 heartbeatLoop 与 logLoop，而 handlePush 和 handleDrain
// 同样是它 go 起来的（issue #61 把这两个改成了异步）。于是那句「serve 返回时
// 它起的循环已经停了」对这两个不成立。
//
// 这不只是注释失准：main 在 serve 返回后立刻重连，新连接的下发会和上一次连接
// 那份还在跑的下发**同时**往 Caddy 上灌配置——pushMu 是 Agent 级的，能拦住
// 两份同时应用，但拦不住次序：最终态取决于谁后到，而两份回执都会说成功。
func TestServeWaitsForThePushItStarted(t *testing.T) {
	_, f, release, done := pushStuckAt(t)

	f.breakIt()
	select {
	case <-done:
		t.Fatal("下发还卡在应用配置那一步，serve 就返回了 —— " +
			"main 这就会重连，下一份配置将与它同时往 Caddy 上灌")
	case <-time.After(150 * time.Millisecond):
	}

	release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("下发跑完了 serve 还不返回")
	}
}
