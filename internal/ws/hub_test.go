package ws

import (
	"encoding/json"
	"testing"
)

func recvFrame(t *testing.T, ch <-chan []byte) Frame {
	t.Helper()
	select {
	case b, ok := <-ch:
		if !ok {
			t.Fatal("通道已关闭，没有收到帧")
		}
		var f Frame
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatalf("帧不是合法 JSON: %v", err)
		}
		return f
	default:
		t.Fatal("没有待读的帧")
		return Frame{}
	}
}

func TestBroadcastReachesAllSubscribers(t *testing.T) {
	h := NewHub(nil)
	a, stopA := h.Subscribe()
	b, stopB := h.Subscribe()
	defer stopA()
	defer stopB()

	h.Broadcast(TypeEvent, Event{Kind: "ok", Msg: "配置下发完成"})

	for i, ch := range []<-chan []byte{a, b} {
		f := recvFrame(t, ch)
		if f.Type != TypeEvent {
			t.Errorf("订阅者 %d 收到 type=%q，想要 %q", i, f.Type, TypeEvent)
		}
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	h := NewHub(nil)
	ch, stop := h.Subscribe()
	stop()

	if n := h.ClientCount(); n != 0 {
		t.Fatalf("退订后仍有 %d 个客户端", n)
	}
	if _, ok := <-ch; ok {
		t.Fatal("退订后通道应当已关闭")
	}
	h.Broadcast(TypeEvent, Event{Msg: "不该崩"}) // 不该 panic
}

// 慢客户端被断开而不是被丢帧。
//
// 这条锁住的是一个刻意的取舍：丢帧会让 deploy_progress 的终态可能丢失，
// 那一行就永远停在「热重载中」——静默且不自愈。断开让前端重连并走 REST
// 重取全量，结果是正确的。
func TestSlowClientIsDroppedNotSilentlyStarved(t *testing.T) {
	h := NewHub(nil)
	ch, stop := h.Subscribe()
	defer stop()

	for i := 0; i < clientBuffer+5; i++ {
		h.Broadcast(TypeHeartbeat, Heartbeat{ID: "node-hk-01"})
	}

	if n := h.ClientCount(); n != 0 {
		t.Fatalf("积压超限的客户端仍在册（%d 个），它看到的状态已经不可信", n)
	}
	// 排空后通道必须是关闭的——读方据此结束，而不是永远阻塞。
	for range ch {
	}
}

// 三类帧的 type 字符串是契约的一部分，前端按它分派。
func TestFrameTypeStringsMatchContract(t *testing.T) {
	for got, want := range map[string]string{
		TypeHeartbeat:      "heartbeat",
		TypeEvent:          "event",
		TypeDeployProgress: "deploy_progress",
	} {
		if got != want {
			t.Errorf("帧类型 %q 与契约 §2 不符，想要 %q", got, want)
		}
	}
}

// deploy_progress 的 retrying 必须出现在线上，否则前端分不清
// 「还会重试」与「终态失败」（ADR-0005）。
func TestDeployProgressCarriesRetrying(t *testing.T) {
	b, err := json.Marshal(DeployProgress{
		DeployID: 81, Node: "node-tw-01", State: "fail",
		Detail: "deadline exceeded", Retrying: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["retrying"]; !ok {
		t.Fatal("deploy_progress 缺少 retrying 字段")
	}
	if m["state"] != "fail" {
		t.Errorf("state = %v", m["state"])
	}
}
