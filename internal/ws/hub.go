// Package ws 是控制台的实时通道。
//
// 三类帧（前端文档 §6）：心跳、事件、下发进度。字段名与文档一致，
// 前端直接用，不做二次映射——映射层是契约悄悄漂开的常见入口。
package ws

import "sync"

// Frame 是一条帧。
type Frame struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// bufferSize 是每个订阅者的缓冲深度。
//
// 有缓冲是为了容忍瞬时抖动；满了就丢帧而不是阻塞——实时通道的帧是**可丢**的，
// 页面下次拉取会拿到权威状态。反过来，广播一旦能阻塞，一个卡住的浏览器页签
// 就能拖住整个下发编排（进度帧正是在编排过程中发出的）。
const bufferSize = 64

type Hub struct {
	mu   sync.RWMutex
	subs map[chan Frame]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: map[chan Frame]struct{}{}}
}

// Subscribe 返回一个只读通道。用完必须 Unsubscribe，否则通道会一直占着缓冲。
func (h *Hub) Subscribe() chan Frame {
	ch := make(chan Frame, bufferSize)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe 退订并关闭通道。幂等——重复退订不会 panic。
func (h *Hub) Unsubscribe(ch chan Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; !ok {
		return
	}
	delete(h.subs, ch)
	close(ch)
}

// Broadcast 把帧发给所有订阅者，**永不阻塞**。
func (h *Hub) Broadcast(f Frame) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- f:
		default:
			// 该订阅者跟不上，丢掉这一帧。丢帧的代价是它的界面短暂落后，
			// 阻塞的代价是所有人的下发都停住——两者不在一个量级。
		}
	}
}

// Subscribers 返回当前订阅者数量，供健康检查与测试使用。
func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
