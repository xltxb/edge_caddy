package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
)

// logCapacity 是 Agent 本地攒着的日志上限。
//
// 满了丢**最旧的**：节点掉线时日志攒不出去，而那段时间里最该被送上来的
// 是掉线之后发生了什么，不是掉线之前的例行记录。
const logCapacity = 500

// logBatchMax 是一次上报的条数上限。
//
// 分批不是为了省带宽（几百条文本很小），是为了让**一次失败只丢一批**：
// 隧道断在中途时，没送出去的那些还在缓冲里，下一次连上会接着送。
const logBatchMax = 100

// LogBuffer 是 Agent 自己运行日志的环形缓冲。
//
// **只收 Agent 自己的日志，不收 Caddy 的 access log。**
// 一台扛流量的边缘节点，access log 是海量的，全量上报会把隧道和主控数据库
// 压垮；而人在控制台上问的是「这台机器上发生了什么」——配置应用、证书加载、
// 校验端点报错，那些都是 Agent 自己写的。access log 属于另一个问题
// （流量分析），它有自己的工具。
type LogBuffer struct {
	mu    sync.Mutex
	lines []*edgev1.LogLine
}

// Handler 把 h 包一层，日志照常落到 h，同时进缓冲。
func (b *LogBuffer) Handler(h slog.Handler) slog.Handler {
	return &captureHandler{Handler: h, buf: b}
}

func (b *LogBuffer) add(l *edgev1.LogLine) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, l)
	if n := len(b.lines) - logCapacity; n > 0 {
		b.lines = b.lines[n:]
	}
}

// take 取走至多 logBatchMax 条。**取走即从缓冲移除**——
// 上报失败时由调用方决定要不要放回去（putBack）。
func (b *LogBuffer) take() []*edgev1.LogLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lines) == 0 {
		return nil
	}
	n := min(len(b.lines), logBatchMax)
	out := b.lines[:n]
	b.lines = b.lines[n:]
	return out
}

// putBack 把没送成的一批放回队首，保持时间顺序。
func (b *LogBuffer) putBack(lines []*edgev1.LogLine) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(lines, b.lines...)
	if n := len(b.lines) - logCapacity; n > 0 {
		b.lines = b.lines[n:]
	}
}

func (b *LogBuffer) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.lines)
}

type captureHandler struct {
	slog.Handler
	buf   *LogBuffer
	attrs []slog.Attr
}

func (h *captureHandler) Handle(ctx context.Context, r slog.Record) error {
	msg := r.Message
	// 把结构化字段拼进正文：控制台上那一栏是一行文本，
	// 而「err=xxx」往往正是那条日志唯一有用的部分。
	appendAttr := func(a slog.Attr) bool {
		msg += " " + a.Key + "=" + a.Value.String()
		return true
	}
	for _, a := range h.attrs {
		appendAttr(a)
	}
	r.Attrs(appendAttr)

	h.buf.add(&edgev1.LogLine{
		AtUnixMs: r.Time.UnixMilli(),
		Level:    levelName(r.Level),
		Msg:      msg,
	})
	return h.Handler.Handle(ctx, r)
}

func (h *captureHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &captureHandler{
		Handler: h.Handler.WithAttrs(as),
		buf:     h.buf,
		attrs:   append(append([]slog.Attr{}, h.attrs...), as...),
	}
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	return &captureHandler{Handler: h.Handler.WithGroup(name), buf: h.buf, attrs: h.attrs}
}

// levelName 用契约 §4 的取值：debug | info | warn | error。
//
// 不用 slog 的 String()（它给 "INFO"/"WARN"）——那会让前端在四个已知取值之外
// 再见到四个大写的，而契约里只列了小写的四个。
func levelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	default:
		return "error"
	}
}

// logFlushInterval 是上报周期。
//
// 不跟心跳走：心跳周期在测试里是 200ms，在生产是几秒，日志跟着它会发出
// 大量小批次。日志不是实时数据，晚几秒到达没有代价。
const logFlushInterval = 5 * time.Second
