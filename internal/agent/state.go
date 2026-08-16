package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LogRingSize 是 Agent 保留的最近日志条数。
//
// 取小值：这不是日志系统，只是「探活时顺手带回最近发生了什么」。
// 真要查历史得上机器看 journald——把它做大只会让每次探活的载荷变胖。
const LogRingSize = 100

// state 是一个 Agent 实例的运行时状态。
//
// 刻意**不是包级全局**：同一个进程里跑两个 Agent（测试里就是这样）时，
// 全局状态会让 A 应用成功的版本被 B 当成自己的，B 明明失败了却上报了
// 一个它根本没生效的版本号，主控那边看到的是「不漂移」。
type state struct {
	mu         sync.RWMutex
	cfgVersion string
	logs       []string
}

func newState() *state {
	return &state{logs: make([]string, 0, LogRingSize)}
}

func (s *state) setCfgVersion(v string) {
	s.mu.Lock()
	s.cfgVersion = v
	s.mu.Unlock()
}

func (s *state) currentCfgVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfgVersion
}

func (s *state) recentLogs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.logs))
	copy(out, s.logs)
	return out
}

func (s *state) appendLog(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, line)
	if len(s.logs) > LogRingSize {
		s.logs = s.logs[len(s.logs)-LogRingSize:]
	}
}

// ringHandler 在正常输出日志的同时，把每条记录留一份在环形缓冲里。
//
// 包一层 slog.Handler 而不是在每个日志点手工 append：手工的那种迟早会漏，
// 而漏掉的恰恰是出问题时最想看到的那几条。
type ringHandler struct {
	inner slog.Handler
	st    *state
	attrs []slog.Attr
}

func (h *ringHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *ringHandler) Handle(ctx context.Context, r slog.Record) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s", r.Time.UTC().Format(time.RFC3339), r.Level, r.Message)
	for _, a := range h.attrs {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	})
	h.st.appendLog(b.String())
	return h.inner.Handle(ctx, r)
}

func (h *ringHandler) WithAttrs(as []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(as))
	merged = append(merged, h.attrs...)
	merged = append(merged, as...)
	return &ringHandler{inner: h.inner.WithAttrs(as), st: h.st, attrs: merged}
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	return &ringHandler{inner: h.inner.WithGroup(name), st: h.st, attrs: h.attrs}
}
