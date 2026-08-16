package alert_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/alert"
	"github.com/xltxb/edge_caddy/internal/ws"
)

// sink 是一个真的 HTTP 接收端。用真服务端而不是打桩 http.Client：
// 「发出去的 JSON 长什么样」正是这一层唯一的产出，打桩会把它整个跳过。
type sink struct {
	srv *httptest.Server

	mu     sync.Mutex
	bodies []map[string]any
	raw    []string
	// status 是下一次响应的状态码序列，用完后恒为最后一个。
	status []int
	hits   int
}

func newSink(t *testing.T, status ...int) *sink {
	t.Helper()
	s := &sink{status: status}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blob, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		s.mu.Lock()
		code := http.StatusOK
		if len(s.status) > 0 {
			if s.hits < len(s.status) {
				code = s.status[s.hits]
			} else {
				code = s.status[len(s.status)-1]
			}
		}
		s.hits++
		s.raw = append(s.raw, string(blob))
		var m map[string]any
		if json.Unmarshal(blob, &m) == nil {
			s.bodies = append(s.bodies, m)
		}
		s.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *sink) last() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		return nil
	}
	return s.bodies[len(s.bodies)-1]
}

func (s *sink) lastRaw() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.raw) == 0 {
		return ""
	}
	return s.raw[len(s.raw)-1]
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func newNotifier(t *testing.T, cfg alert.Config) *alert.Notifier {
	t.Helper()
	n := alert.New(alert.Deps{})
	n.SetConfig(cfg)
	t.Cleanup(n.Close)
	return n
}

// 低于阈值的事件不发送。
func TestLevelFilterDropsEventsBelowThreshold(t *testing.T) {
	hook := newSink(t)
	n := newNotifier(t, alert.Config{Enabled: true, MinLevel: alert.LevelCrit, WebhookURL: hook.srv.URL})

	n.Notify(alert.Event{Node: "node-a", Kind: "warn", Msg: "配置漂移"})
	n.Notify(alert.Event{Node: "node-b", Kind: "crit", Msg: "心跳连续超时"})

	if !waitFor(2*time.Second, func() bool { return hook.count() >= 1 }) {
		t.Fatal("严重事件应被发送")
	}
	// 再等一会儿，确认 warn 那条确实没有偷偷发出去
	time.Sleep(150 * time.Millisecond)
	if hook.count() != 1 {
		t.Fatalf("仅严重阈值下只该发 1 条，实际 %d 条", hook.count())
	}
	if !strings.Contains(hook.lastRaw(), "node-b") {
		t.Errorf("发出去的应是严重那条，实际 %s", hook.lastRaw())
	}
}

// 恢复通知**不受**级别过滤，只要此前为该节点报过警。
//
// 只报警不报恢复的话，群里永远挂着一条没有下文的告警，人得自己去面板确认
// 是不是还挂着——那正是告警本该替他省掉的事。
func TestRecoveryIsSentEvenBelowThreshold(t *testing.T) {
	hook := newSink(t)
	n := newNotifier(t, alert.Config{Enabled: true, MinLevel: alert.LevelCrit, WebhookURL: hook.srv.URL})

	n.Notify(alert.Event{Node: "node-a", Kind: "crit", Msg: "心跳连续超时"})
	if !waitFor(2*time.Second, func() bool { return hook.count() == 1 }) {
		t.Fatal("严重事件应被发送")
	}
	n.Notify(alert.Event{Node: "node-a", Kind: "ok", Msg: "心跳恢复"})
	if !waitFor(2*time.Second, func() bool { return hook.count() == 2 }) {
		t.Fatalf("报过警的节点恢复时应发闭环通知，实际共 %d 条", hook.count())
	}
	if !strings.Contains(hook.lastRaw(), "恢复") {
		t.Errorf("最后一条应是恢复通知，实际 %s", hook.lastRaw())
	}
}

// 没报过警的节点，它的 ok 事件在高阈值下不该发——否则每次节点重连都刷一条。
func TestRecoveryWithoutPriorAlertIsNotSent(t *testing.T) {
	hook := newSink(t)
	n := newNotifier(t, alert.Config{Enabled: true, MinLevel: alert.LevelCrit, WebhookURL: hook.srv.URL})

	n.Notify(alert.Event{Node: "node-a", Kind: "ok", Msg: "心跳恢复"})
	time.Sleep(200 * time.Millisecond)
	if hook.count() != 0 {
		t.Fatalf("没报过警就没有需要闭环的东西，不该发，实际 %d 条", hook.count())
	}
}

// 两个渠道并行发，一个挂了不影响另一个。
func TestChannelsAreIndependent(t *testing.T) {
	hook := newSink(t, http.StatusInternalServerError) // 一直 500
	lark := newSink(t)
	n := newNotifier(t, alert.Config{
		Enabled: true, MinLevel: alert.LevelAll,
		WebhookURL: hook.srv.URL, LarkURL: lark.srv.URL,
		MaxRetries: 1,
	})

	n.Notify(alert.Event{Node: "node-a", Kind: "crit", Msg: "心跳连续超时"})

	if !waitFor(3*time.Second, func() bool { return lark.count() >= 1 }) {
		t.Fatal("Webhook 挂了不该影响 Lark")
	}
	// Webhook 那条还在重试，等它把重试跑完
	if !waitFor(3*time.Second, func() bool { return n.Stats().Failed > 0 }) {
		t.Error("失败要能被发现，不能静默吞掉")
	}
}

// Lark 收到的是消息卡片，不是纯文本。
func TestLarkReceivesInteractiveCard(t *testing.T) {
	lark := newSink(t)
	n := newNotifier(t, alert.Config{Enabled: true, MinLevel: alert.LevelAll, LarkURL: lark.srv.URL})

	n.Notify(alert.Event{Node: "node-hk-01", Kind: "crit", Msg: "心跳连续超时，判定为离线"})
	if !waitFor(2*time.Second, func() bool { return lark.count() == 1 }) {
		t.Fatal("Lark 应收到消息")
	}
	body := lark.last()
	if body["msg_type"] != "interactive" {
		t.Fatalf("应为消息卡片（interactive），实际 msg_type=%v", body["msg_type"])
	}
	if body["card"] == nil {
		t.Fatal("卡片内容缺失")
	}
	raw := lark.lastRaw()
	for _, want := range []string{"node-hk-01", "心跳连续超时"} {
		if !strings.Contains(raw, want) {
			t.Errorf("卡片里应包含 %q，实际 %s", want, raw)
		}
	}
}

// 严重告警在开启该选项时 @所有人；未开启时不 @。
func TestAtAllOnlyOnCritAndOnlyWhenEnabled(t *testing.T) {
	lark := newSink(t)
	n := newNotifier(t, alert.Config{
		Enabled: true, MinLevel: alert.LevelAll, LarkURL: lark.srv.URL, AtAllOnCrit: true,
	})

	n.Notify(alert.Event{Node: "node-a", Kind: "warn", Msg: "配置漂移"})
	if !waitFor(2*time.Second, func() bool { return lark.count() == 1 }) {
		t.Fatal("警告应发出")
	}
	if strings.Contains(lark.lastRaw(), "at id=all") {
		t.Error("只有严重告警才 @所有人——警告也 @ 的话，很快就没人看了")
	}

	n.Notify(alert.Event{Node: "node-a", Kind: "crit", Msg: "心跳连续超时"})
	if !waitFor(2*time.Second, func() bool { return lark.count() == 2 }) {
		t.Fatal("严重告警应发出")
	}
	if !strings.Contains(lark.lastRaw(), "at id=all") {
		t.Errorf("开启后严重告警应 @所有人，实际 %s", lark.lastRaw())
	}

	// 关掉之后不再 @
	n.SetConfig(alert.Config{Enabled: true, MinLevel: alert.LevelAll, LarkURL: lark.srv.URL, AtAllOnCrit: false})
	n.Notify(alert.Event{Node: "node-a", Kind: "crit", Msg: "又挂了"})
	if !waitFor(2*time.Second, func() bool { return lark.count() == 3 }) {
		t.Fatal("严重告警应发出")
	}
	if strings.Contains(lark.lastRaw(), "at id=all") {
		t.Error("关闭该选项后不该 @所有人")
	}
}

// 重试有上限，且只重试值得重试的失败。
func TestRetriesAreBoundedAndOnlyForRetryableFailures(t *testing.T) {
	// 一直 500：可重试
	flaky := newSink(t, http.StatusInternalServerError)
	n := newNotifier(t, alert.Config{
		Enabled: true, MinLevel: alert.LevelAll, WebhookURL: flaky.srv.URL, MaxRetries: 2,
	})
	n.Notify(alert.Event{Node: "node-a", Kind: "crit", Msg: "x"})
	// 首次 + 2 次重试 = 3
	if !waitFor(3*time.Second, func() bool { return flaky.count() >= 3 }) {
		t.Fatalf("应重试到上限，实际 %d 次", flaky.count())
	}
	// 退避是线性的（200ms×次数），等够第 4、5 次本该发生的时刻再断言。
	// 只等 300ms 的话，超出上限的重试还没来得及发出，这条断言等于没写。
	time.Sleep(1500 * time.Millisecond)
	if flaky.count() > 3 {
		t.Fatalf("重试必须有上限，实际发了 %d 次", flaky.count())
	}

	// 401：地址或凭据配错了，重试一万次也是 401，只会把日志刷满
	bad := newSink(t, http.StatusUnauthorized)
	n2 := newNotifier(t, alert.Config{
		Enabled: true, MinLevel: alert.LevelAll, WebhookURL: bad.srv.URL, MaxRetries: 3,
	})
	n2.Notify(alert.Event{Node: "node-a", Kind: "crit", Msg: "x"})
	if !waitFor(2*time.Second, func() bool { return bad.count() >= 1 }) {
		t.Fatal("应发出一次")
	}
	time.Sleep(400 * time.Millisecond)
	if bad.count() != 1 {
		t.Fatalf("凭据错误不该重试，实际发了 %d 次", bad.count())
	}
}

// 渠道慢或挂掉时**不阻塞**调用方。
//
// 告警是从心跳巡检和下发编排里发出来的。在那里同步等一个卡住的 Webhook，
// 等于让一个第三方服务能停掉整个主控。
func TestNotifyNeverBlocksCaller(t *testing.T) {
	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block // 永远不回，直到测试结束
	}))
	defer slow.Close()
	defer close(block)

	n := newNotifier(t, alert.Config{Enabled: true, MinLevel: alert.LevelAll, WebhookURL: slow.URL})

	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			n.Notify(alert.Event{Node: "node-a", Kind: "crit", Msg: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("投递告警阻塞了调用方——一个卡住的第三方服务能停掉整个主控")
	}
}

// 未配置任何渠道时不做无谓的工作，也不报错。
func TestNoChannelsConfiguredIsNotAnError(t *testing.T) {
	n := newNotifier(t, alert.Config{Enabled: true, MinLevel: alert.LevelAll})
	n.Notify(alert.Event{Node: "node-a", Kind: "crit", Msg: "x"})
	time.Sleep(150 * time.Millisecond)
	if s := n.Stats(); s.Failed != 0 {
		t.Errorf("没配渠道不算失败，实际 failed=%d", s.Failed)
	}
}

// 整体关闭时一条都不发。
func TestDisabledSendsNothing(t *testing.T) {
	hook := newSink(t)
	n := newNotifier(t, alert.Config{Enabled: false, MinLevel: alert.LevelAll, WebhookURL: hook.srv.URL})
	n.Notify(alert.Event{Node: "node-a", Kind: "crit", Msg: "x"})
	time.Sleep(200 * time.Millisecond)
	if hook.count() != 0 {
		t.Fatalf("已关闭不该发送，实际 %d 条", hook.count())
	}
}

// Broadcast 把事件帧转成告警，同时原样转给控制台通道。
//
// 挂在 Broadcaster 这一层而不是让每个发事件的地方各调一次告警：
// 后者迟早会漏掉一处，而漏掉的那处恰恰是没人想到的那类事件。
func TestBroadcastTeesEventsToBothConsoleAndAlerts(t *testing.T) {
	hook := newSink(t)
	inner := &recorder{}
	n := alert.New(alert.Deps{Inner: inner})
	defer n.Close()
	n.SetConfig(alert.Config{Enabled: true, MinLevel: alert.LevelAll, WebhookURL: hook.srv.URL})

	n.Broadcast(ws.Frame{Type: "event", Data: map[string]any{
		"node": "node-a", "kind": "crit", "msg": "心跳连续超时",
	}})
	// 心跳帧不是告警，但必须照样转给控制台
	n.Broadcast(ws.Frame{Type: "heartbeat", Data: map[string]any{"id": "node-a", "cpu": 1.0}})

	if !waitFor(2*time.Second, func() bool { return hook.count() == 1 }) {
		t.Fatalf("事件帧应产生一条告警，实际 %d 条", hook.count())
	}
	if got := inner.len(); got != 2 {
		t.Errorf("两条帧都该原样转给控制台，实际 %d 条", got)
	}
}

// 测试卡片走与真实告警相同的发送路径。
func TestSendTestCardUsesTheSamePath(t *testing.T) {
	lark := newSink(t)
	n := newNotifier(t, alert.Config{Enabled: true, MinLevel: alert.LevelCrit, LarkURL: lark.srv.URL})

	// 阈值是「仅严重」，但测试卡片是人主动点的，不该被级别过滤挡掉
	if err := n.SendTest(context.Background()); err != nil {
		t.Fatalf("发送测试卡片失败: %v", err)
	}
	if lark.count() != 1 {
		t.Fatalf("应发出 1 张测试卡片，实际 %d", lark.count())
	}
	if body := lark.last(); body["msg_type"] != "interactive" {
		t.Errorf("测试卡片也该是消息卡片，实际 %v", body["msg_type"])
	}
}

// 测试发送要同步返回结果：人点了按钮就是在等这个答案。
func TestSendTestReportsFailureSynchronously(t *testing.T) {
	dead := newSink(t, http.StatusInternalServerError)
	n := newNotifier(t, alert.Config{Enabled: true, MinLevel: alert.LevelAll, LarkURL: dead.srv.URL, MaxRetries: 0})

	if err := n.SendTest(context.Background()); err == nil {
		t.Fatal("渠道不通时测试发送应报错，而不是返回成功让人以为配好了")
	}
}

type recorder struct {
	mu     sync.Mutex
	frames []ws.Frame
}

func (r *recorder) Broadcast(f ws.Frame) {
	r.mu.Lock()
	r.frames = append(r.frames, f)
	r.mu.Unlock()
}

func (r *recorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}
