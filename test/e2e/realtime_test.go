package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/health"
	"github.com/xltxb/edge_caddy/internal/render"
)

// 真 Agent 的心跳经隧道变成 WS 帧，订阅者收得到。
//
// 补的是一个明确的缺口：心跳帧此前只有单测覆盖，从没在真链路上跑过——
// WS 通道做完了、Agent 也在发心跳，但两端没接起来验证过。
func TestHeartbeatReachesSubscribersOnRealLink(t *testing.T) {
	m := startMaster(t, render.Options{Listen: []string{"127.0.0.1:0"}})
	frames := m.hub.Subscribe()
	defer m.hub.Unsubscribe(frames)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	joinAgent(t, ctx, m, "node-hb-01", 0) // 不需要 Caddy，只发心跳

	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 1 }) {
		t.Fatal("Agent 未接入")
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-frames:
			if f.Type != "heartbeat" {
				continue
			}
			id, _ := f.Data["id"].(string)
			if id != "node-hb-01" {
				t.Fatalf("心跳帧的节点标识不对: %v", f.Data)
			}
			// 字段名必须与前端文档 §6 一致——前端直接用，不做二次映射
			for _, k := range []string{"id", "cpu", "mem", "conns"} {
				if _, has := f.Data[k]; !has {
					t.Errorf("心跳帧缺少字段 %q: %v", k, f.Data)
				}
			}
			return
		case <-deadline:
			t.Fatal("5 秒内没有收到任何心跳帧——隧道与实时通道没有接起来")
		}
	}
}

// 节点掉线时，事件帧经同一条通道到达订阅者。
func TestOfflineEventReachesSubscribers(t *testing.T) {
	m := startMaster(t, render.Options{Listen: []string{"127.0.0.1:0"}})
	frames := m.hub.Subscribe()
	defer m.hub.Unsubscribe(frames)

	ctx, cancel := context.WithCancel(context.Background())
	joinAgent(t, ctx, m, "node-gone-01", 0)
	if !waitFor(5*time.Second, func() bool { return len(m.tun.Connected()) == 1 }) {
		t.Fatal("Agent 未接入")
	}
	// 断开 Agent，让心跳停下来
	cancel()

	// 用很短的阈值跑巡检，避免测试等太久
	checker := health.New(m.st, m.hub, health.Config{
		Interval: 100 * time.Millisecond, MissedBeats: 3,
	})
	found := waitFor(6*time.Second, func() bool {
		checker.Sweep(context.Background())
		for {
			select {
			case f := <-frames:
				if f.Type == "event" && f.Data["node"] == "node-gone-01" {
					return true
				}
			default:
				return false
			}
		}
	})
	if !found {
		t.Fatal("节点掉线后应有事件帧到达订阅者")
	}
}
