package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/alert"
	"github.com/xltxb/edge_caddy/internal/health"
	"github.com/xltxb/edge_caddy/internal/render"
)

// 节点掉线这一真实场景端到端走通：真 Agent 接入 → 断开 → 巡检判离线 →
// 级别过滤 → 真 HTTP 端点收到 Lark 卡片 → 节点回来后收到恢复通知。
//
// 单测能覆盖每一段，但覆盖不了「串起来之后事件的 kind 到底是什么」——
// health 发的是 crit，过滤器认的也得是 crit。这类契约只有真跑一遍才知道。
func TestNodeDownAlertReachesLarkEndToEnd(t *testing.T) {
	caddyBin := findCaddy(t)
	edgePort, adminPort := freePort(t), freePort(t)
	startCaddy(t, caddyBin, t.TempDir(), adminPort)

	lark := newCardSink(t)

	m := startMaster(t, render.Options{Listen: []string{fmt.Sprintf("127.0.0.1:%d", edgePort)}})

	// 告警器包住 hub：事件帧原样转给控制台，同时进告警队列
	notifier := alert.New(alert.Deps{Inner: m.hub})
	defer notifier.Close()
	notifier.SetConfig(alert.Config{
		Enabled: true, MinLevel: alert.LevelCrit,
		LarkURL: lark.srv.URL, AtAllOnCrit: true, MaxRetries: 1,
	})

	// 巡检器把状态变化发给告警器，而不是直接发给 hub
	checker := health.New(m.st, notifier, health.Config{
		Interval: 100 * time.Millisecond, MissedBeats: 2,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agentCtx, stopAgent := context.WithCancel(ctx)
	joinAgent(t, agentCtx, m, "node-alert-01", adminPort)
	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 1 }) {
		t.Fatal("Agent 未能接入")
	}
	// 等心跳落库，否则节点会被当成「还没接入」而不是「掉了」
	if !waitFor(3*time.Second, func() bool {
		ns, err := m.st.ListNodes(ctx)
		return err == nil && len(ns) == 1 && !ns[0].LastHB.IsZero()
	}) {
		t.Fatal("节点心跳未落库")
	}

	// 把 Agent 停掉：真的断开，不是伪造一个状态
	stopAgent()
	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 0 }) {
		t.Fatal("Agent 未断开")
	}

	// 巡检跑到判离线
	if !waitFor(5*time.Second, func() bool {
		checker.Sweep(ctx)
		return lark.count() >= 1
	}) {
		t.Fatal("节点掉线应产生一条 Lark 告警")
	}

	body := lark.last()
	if body["msg_type"] != "interactive" {
		t.Errorf("应为消息卡片，实际 %v", body["msg_type"])
	}
	raw := lark.lastRaw()
	for _, want := range []string{"node-alert-01", "心跳", "at id=all"} {
		if !strings.Contains(raw, want) {
			t.Errorf("卡片里应包含 %q，实际:\n%s", want, raw)
		}
	}
	if !strings.Contains(raw, "red") {
		t.Errorf("严重告警的卡片应为红色标头，实际:\n%s", raw)
	}

	// 节点回来：应收到恢复通知，且**不**再 @所有人
	joinAgent(t, ctx, m, "node-alert-01", adminPort)
	if !waitFor(8*time.Second, func() bool {
		checker.Sweep(ctx)
		return lark.count() >= 2
	}) {
		t.Fatal("节点恢复后应收到闭环通知，否则群里永远挂着一条没有下文的告警")
	}
	rec := lark.lastRaw()
	if !strings.Contains(rec, "恢复") {
		t.Errorf("最后一条应是恢复通知，实际:\n%s", rec)
	}
	if strings.Contains(rec, "at id=all") {
		t.Error("恢复通知不该 @所有人")
	}
	if n := notifier.Stats().Failed; n != 0 {
		t.Errorf("渠道是通的，不该有失败，实际 %d", n)
	}
}

// 渠道挂掉时主流程照常：节点仍被判离线、状态仍落库，只是告警发不出去且计数可见。
func TestBrokenAlertChannelDoesNotBlockHealthChecks(t *testing.T) {
	edgePort := freePort(t)
	m := startMaster(t, render.Options{Listen: []string{fmt.Sprintf("127.0.0.1:%d", edgePort)}})

	// 一个永远不回应的端点
	block := make(chan struct{})
	stuck := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer stuck.Close()

	notifier := alert.New(alert.Deps{Inner: m.hub})
	defer notifier.Close()
	// 这个 defer 必须在 notifier.Close 之后声明：defer 是后进先出，
	// 要先放开卡住的请求，Close 才不会在那儿干等一整个发送超时。
	defer close(block)
	notifier.SetConfig(alert.Config{
		Enabled: true, MinLevel: alert.LevelAll, WebhookURL: stuck.URL, MaxRetries: 0,
	})
	checker := health.New(m.st, notifier, health.Config{
		Interval: 50 * time.Millisecond, MissedBeats: 1,
	})

	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	if err := m.st.UpsertNodeSeen(ctx, "node-stuck-01", "cfg-1", past); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		checker.Sweep(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("巡检被卡住的告警渠道拖住了——一个第三方服务不该能停掉主控的巡检")
	}

	ns, err := m.st.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ns[0].Status != "down" {
		t.Errorf("告警发不出去不影响判离线，节点状态应为 down，实际 %s", ns[0].Status)
	}
}

// ── 装置 ──

type cardSink struct {
	srv *httptest.Server
	mu  sync.Mutex
	raw []string
}

func newCardSink(t *testing.T) *cardSink {
	t.Helper()
	s := &cardSink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blob, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		s.mu.Lock()
		s.raw = append(s.raw, string(blob))
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *cardSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.raw)
}

func (s *cardSink) lastRaw() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.raw) == 0 {
		return ""
	}
	return s.raw[len(s.raw)-1]
}

func (s *cardSink) last() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(s.lastRaw()), &m)
	return m
}
