package ws_test

import (
	"sync"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/ws"
)

// 一个卡住的订阅者不得拖住广播。
//
// 这是这个包唯一真正的风险：浏览器页签被切到后台、网络卡顿、或者干脆是个
// 挂起的连接——只要广播会阻塞，一个慢客户端就能把主控的下发编排整个拖住。
// 下发进度帧正是在编排过程中发出的，拖住它等于拖住下发本身。
func TestSlowSubscriberDoesNotBlockBroadcast(t *testing.T) {
	h := ws.NewHub()

	// 一个永不读取的订阅者
	stuck := h.Subscribe()
	defer h.Unsubscribe(stuck)
	// 一个正常读取的订阅者
	fast := h.Subscribe()
	defer h.Unsubscribe(fast)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// 发得远超任何合理缓冲，卡住的那个必然满
		for i := 0; i < 500; i++ {
			h.Broadcast(ws.Frame{Type: "event", Data: map[string]any{"i": i}})
		}
	}()

	// 同时把 fast 的帧读掉
	go func() {
		for range fast {
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("广播被慢订阅者阻塞了——一个卡住的页签会拖住整个下发编排")
	}
}

// 帧要送到每一个订阅者。
func TestBroadcastReachesAllSubscribers(t *testing.T) {
	h := ws.NewHub()
	a, b := h.Subscribe(), h.Subscribe()
	defer h.Unsubscribe(a)
	defer h.Unsubscribe(b)

	h.Broadcast(ws.Frame{Type: "heartbeat", Data: map[string]any{"id": "node-hk-01"}})

	for name, ch := range map[string]<-chan ws.Frame{"a": a, "b": b} {
		select {
		case f := <-ch:
			if f.Type != "heartbeat" {
				t.Errorf("%s 收到的帧类型不对: %s", name, f.Type)
			}
		case <-time.After(time.Second):
			t.Errorf("%s 没收到帧", name)
		}
	}
}

// 退订后不再收到帧，且通道被关闭——读取方靠它退出循环。
func TestUnsubscribeClosesChannel(t *testing.T) {
	h := ws.NewHub()
	ch := h.Subscribe()
	h.Unsubscribe(ch)

	select {
	case _, open := <-ch:
		if open {
			t.Fatal("退订后通道应被关闭")
		}
	case <-time.After(time.Second):
		t.Fatal("退订后通道未关闭，读取方会永远挂着")
	}

	// 退订是幂等的：重复退订不该 panic
	h.Unsubscribe(ch)
}

// 广播与订阅/退订并发时不得竞态或 panic。
//
// 真实场景就是并发的：下发编排在发进度帧，同时浏览器在开新页签、关旧页签。
func TestConcurrentSubscribeAndBroadcast(t *testing.T) {
	h := ws.NewHub()
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			h.Broadcast(ws.Frame{Type: "deploy_progress", Data: map[string]any{"i": i}})
		}
	}()

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				ch := h.Subscribe()
				select {
				case <-ch:
				default:
				}
				h.Unsubscribe(ch)
			}
		}()
	}
	wg.Wait()
}
