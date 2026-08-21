// Package ws 是控制台的实时通道：一个扇出 hub 加三类帧。
//
// 契约见 docs/api-contract.md §2。这里只送**增量**——首屏数据走 REST
// （GET /overview + GET /nodes）。不做初始快照帧的理由有两条：首屏因此不依赖
// WS 建连速度（PRD 要求 ≤1s），而且不必维护两条产出同样数据的代码路径。
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
)

// 三类帧的 type 值。前端按它分派（api-contract §2）。
const (
	TypeHeartbeat      = "heartbeat"
	TypeEvent          = "event"
	TypeDeployProgress = "deploy_progress"
)

type Frame struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Heartbeat 每节点每个心跳周期一帧。
type Heartbeat struct {
	ID         string  `json:"id"`
	Status     string  `json:"status"`
	CPU        float64 `json:"cpu"`
	Mem        float64 `json:"mem"`
	Conns      uint32  `json:"conns"`
	HBAgeMS    int64   `json:"hb_age_ms"` // 服务端算的年龄；前端收到后本地计时，不用浏览器时钟
	CfgVersion string  `json:"cfg_version"`
	Routes     uint32  `json:"routes"` // 该节点当前生效配置里的数量，漂移节点会报旧数字
	Rules      uint32  `json:"rules"`
}

// Event 的 Kind 是四档：ok = 成功完成的动作，info = 流水账，warn，crit。
// ok 与 info 合并会让下发成功和背景噪音同色（api-contract §2）。
type Event struct {
	ID   int64  `json:"id"`
	At   string `json:"at"`   // RFC3339
	Node string `json:"node"` // 空串表示系统级事件，序列化后前端按 null 处理
	Kind string `json:"kind"`
	Msg  string `json:"msg"`
}

// DeployProgress 的 Retrying 决定前端那一行还会不会再动：
// 节点未回应 → true，后面还有帧；节点回应了但 Caddy 拒绝 → false，
// Detail 是 Caddy 原文，这一行到此为止（ADR-0005）。
type DeployProgress struct {
	DeployID   int64  `json:"deploy_id"`
	CfgVersion string `json:"cfg_version"`
	Node       string `json:"node"`
	State      string `json:"state"` // wait | run | ok | fail
	Detail     string `json:"detail"`
	Retrying   bool   `json:"retrying"`
}

// clientBuffer 是每个连接的积压上限。
//
// 满了之后**关掉这个客户端**而不是丢帧。丢帧看起来更温和，但代价是
// deploy_progress 的终态可能丢掉，那一行会永远停在「热重载中」——一个
// 静默且不会自愈的错误状态。关掉连接会让前端重连并走 REST 重新取全量，
// 结果是正确的。前端本来就有断线降级（2s 轮询 GET /deploys/:id）。
const clientBuffer = 64

type Hub struct {
	log *slog.Logger

	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{log: log, clients: map[chan []byte]struct{}{}}
}

// Subscribe 注册一个订阅者，返回收帧通道与退订函数。
// 通道在退订或被踢时关闭，读方据此结束。
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, clientBuffer)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	n := len(h.clients)
	h.mu.Unlock()
	h.log.Debug("ws 订阅", "clients", n)

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := h.clients[ch]; ok {
				delete(h.clients, ch)
				close(ch)
			}
			h.mu.Unlock()
		})
	}
}

// Broadcast 把一帧扇出给全部订阅者。
// 序列化失败只记日志不 panic：一帧发不出去不该拖垮调用方（心跳、下发调度器）。
func (h *Hub) Broadcast(typ string, data any) {
	b, err := json.Marshal(Frame{Type: typ, Data: data})
	if err != nil {
		h.log.Error("序列化 ws 帧失败", "type", typ, "err", err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- b:
		default:
			// 积压满 = 这个客户端已经落后太多，它看到的状态不可信了。
			h.log.Warn("ws 客户端积压超限，断开以促其重连", "type", typ)
			delete(h.clients, ch)
			close(ch)
		}
	}
}

func (h *Hub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// CloseAll 在主控关停时断开全部订阅者。
func (h *Hub) CloseAll(_ context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		delete(h.clients, ch)
		close(ch)
	}
}
