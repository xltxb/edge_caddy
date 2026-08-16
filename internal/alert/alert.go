// Package alert 把系统事件按级别过滤后送到外部渠道。
//
// 它挂在 Broadcaster 这一层（包住 ws.Hub），而不是让每个发事件的地方各调一次
// 告警：后者迟早会漏掉一处，而漏掉的那处恰恰是没人想到的那类事件。
package alert

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xltxb/edge_caddy/internal/ws"
)

// Level 是通知级别阈值。
type Level string

const (
	// LevelAll 发送全部事件。
	LevelAll Level = "all"
	// LevelWarn 只发送警告及以上。
	LevelWarn Level = "warn"
	// LevelCrit 只发送严重告警。
	LevelCrit Level = "crit"
)

// rank 把事件种类与阈值放到同一把尺子上。
func rank(s string) int {
	switch s {
	case "crit":
		return 2
	case "warn":
		return 1
	default: // ok / info
		return 0
	}
}

func (l Level) threshold() int {
	switch l {
	case LevelCrit:
		return 2
	case LevelWarn:
		return 1
	default:
		return 0
	}
}

// Config 是告警设置。凭据字段（WebhookURL / LarkURL / LarkSecret）**只写入不回显**。
type Config struct {
	Enabled     bool   `json:"enabled"`
	MinLevel    Level  `json:"min_level"`
	WebhookURL  string `json:"webhook_url"`
	LarkURL     string `json:"lark_url"`
	LarkSecret  string `json:"lark_secret"`
	AtAllOnCrit bool   `json:"at_all_on_crit"`
	// MaxRetries 是单个渠道的重试次数上限（不含首次）。
	MaxRetries int `json:"max_retries"`

	// 清除标记只在提交时有意义，不落库（见 Merge）。
	ClearWebhook    bool `json:"-"`
	ClearLark       bool `json:"-"`
	ClearLarkSecret bool `json:"-"`
}

// Event 是一条待通知的事件。
type Event struct {
	Node string
	// Kind 为 ok / warn / crit，与控制台事件流一致。
	Kind string
	Msg  string
	At   time.Time
}

// Stats 是可观测的投递计数。失败必须能被发现——静默失败的告警系统
// 比没有告警更糟：人以为自己被保护着。
type Stats struct {
	Sent    int64
	Failed  int64
	Dropped int64
}

// Deps 是构造依赖。
type Deps struct {
	// Inner 是控制台实时通道。Broadcast 收到的帧原样转给它。
	Inner  Broadcaster
	Logger *slog.Logger
	// Now 可替换时钟，仅供测试注入。
	Now func() time.Time
}

// Broadcaster 是控制台实时通道。
type Broadcaster interface {
	Broadcast(f ws.Frame)
}

// queueSize 是待发队列深度。
//
// 满了就丢弃并计数，绝不阻塞：告警是从心跳巡检和下发编排里发出来的，
// 在那里同步等一个卡住的 Webhook，等于让一个第三方服务能停掉整个主控。
const queueSize = 256

type Notifier struct {
	inner Broadcaster
	log   *slog.Logger
	now   func() time.Time

	mu  sync.RWMutex
	cfg Config
	// alerted 记录已为哪些节点报过警，用于决定恢复通知是否要发。
	alerted map[string]bool

	queue  chan Event
	sender *sender

	sent, failed, dropped atomic.Int64

	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
}

func New(d Deps) *Notifier {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	n := &Notifier{
		inner: d.Inner, log: log, now: now,
		alerted: map[string]bool{},
		queue:   make(chan Event, queueSize),
		sender:  newSender(log),
		done:    make(chan struct{}),
	}
	n.wg.Add(1)
	go n.loop()
	return n
}

// SetConfig 替换设置。可在运行中调用。
func (n *Notifier) SetConfig(c Config) {
	if c.MinLevel == "" {
		c.MinLevel = LevelWarn
	}
	n.mu.Lock()
	n.cfg = c
	n.mu.Unlock()
}

// Config 返回当前设置。调用方**不得**把它直接回给接口——里面有明文凭据。
func (n *Notifier) Config() Config {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.cfg
}

func (n *Notifier) Stats() Stats {
	return Stats{Sent: n.sent.Load(), Failed: n.failed.Load(), Dropped: n.dropped.Load()}
}

// Broadcast 实现 Broadcaster：帧原样转给控制台，事件帧另外进告警队列。
func (n *Notifier) Broadcast(f ws.Frame) {
	if n.inner != nil {
		n.inner.Broadcast(f)
	}
	if f.Type != "event" {
		return
	}
	node, _ := f.Data["node"].(string)
	kind, _ := f.Data["kind"].(string)
	msg, _ := f.Data["msg"].(string)
	n.Notify(Event{Node: node, Kind: kind, Msg: msg})
}

// Notify 投递一条事件。**绝不阻塞**：队列满时丢弃并计数。
func (n *Notifier) Notify(ev Event) {
	if ev.At.IsZero() {
		ev.At = n.now()
	}
	if !n.shouldSend(ev) {
		return
	}
	select {
	case n.queue <- ev:
	default:
		n.dropped.Add(1)
		n.log.Error("告警队列已满，丢弃事件", "node", ev.Node, "kind", ev.Kind, "msg", ev.Msg)
	}
}

// shouldSend 做级别过滤，并维护「报过警的节点」集合。
//
// 恢复通知不受阈值限制，但要求此前真为该节点报过警：
//   - 报过警却不报恢复，群里就永远挂着一条没有下文的告警，人得自己去面板确认
//     是不是还挂着——那正是告警本该替他省掉的事。
//   - 没报过警的 ok 事件则要压住，否则每次节点重连都刷一条。
func (n *Notifier) shouldSend(ev Event) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.cfg.Enabled {
		return false
	}
	if rank(ev.Kind) == 0 {
		if !n.alerted[ev.Node] {
			return false
		}
		delete(n.alerted, ev.Node)
		return true
	}
	if rank(ev.Kind) < n.cfg.MinLevel.threshold() {
		return false
	}
	n.alerted[ev.Node] = true
	return true
}

func (n *Notifier) loop() {
	defer n.wg.Done()
	for {
		select {
		case <-n.done:
			return
		case ev := <-n.queue:
			n.dispatch(context.Background(), ev)
		}
	}
}

// dispatch 并行发两个渠道。一个挂了不影响另一个——它们是两条独立的通路，
// 把它们串起来意味着第一条超时就能拖垮第二条。
func (n *Notifier) dispatch(ctx context.Context, ev Event) {
	cfg := n.Config()
	var wg sync.WaitGroup
	for _, ch := range n.channels(cfg) {
		wg.Add(1)
		go func(c channel) {
			defer wg.Done()
			if err := n.sender.send(ctx, c, ev, cfg); err != nil {
				n.failed.Add(1)
				n.log.Error("告警发送失败", "channel", c.name, "node", ev.Node, "err", err)
				return
			}
			n.sent.Add(1)
		}(ch)
	}
	wg.Wait()
}

func (n *Notifier) channels(cfg Config) []channel {
	var out []channel
	if cfg.WebhookURL != "" {
		out = append(out, channel{name: "webhook", url: cfg.WebhookURL, kind: kindWebhook})
	}
	if cfg.LarkURL != "" {
		out = append(out, channel{name: "lark", url: cfg.LarkURL, kind: kindLark})
	}
	return out
}

// ErrNoChannel 表示一个渠道都没配。
var ErrNoChannel = errors.New("尚未配置任何通知渠道")

// SendTest 发一张测试卡片，**同步**返回结果。
//
// 人点了「发送测试」就是在等这个答案。异步的话按钮永远显示成功，
// 而配错了的渠道要等到真出事那天才被发现。
//
// 它走与真实告警完全相同的发送路径，只跳过级别过滤——测试是人主动点的。
func (n *Notifier) SendTest(ctx context.Context) error {
	cfg := n.Config()
	chs := n.channels(cfg)
	if len(chs) == 0 {
		return ErrNoChannel
	}
	ev := Event{
		Node: "—", Kind: "warn", At: n.now(),
		Msg: "这是一条测试通知。收到它说明该渠道配置正确。",
	}
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	for _, ch := range chs {
		wg.Add(1)
		go func(c channel) {
			defer wg.Done()
			if err := n.sender.send(ctx, c, ev, cfg); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", c.name, err))
				mu.Unlock()
				n.failed.Add(1)
				return
			}
			n.sent.Add(1)
		}(ch)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Close 停掉后台发送。幂等。
func (n *Notifier) Close() {
	n.closeOnce.Do(func() {
		close(n.done)
		n.wg.Wait()
	})
}
