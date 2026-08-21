package agent

import (
	"log/slog"
	"testing"
)

func TestLevelNamesMatchContract(t *testing.T) {
	// 契约 §4 只列了这四个小写取值。slog 的 String() 给的是 "INFO"，
	// 直接透出去会让前端在四个已知取值之外再见到四个大写的。
	cases := map[slog.Level]string{
		slog.LevelDebug: "debug", slog.LevelInfo: "info",
		slog.LevelWarn: "warn", slog.LevelError: "error",
	}
	for lv, want := range cases {
		if got := levelName(lv); got != want {
			t.Errorf("%v → %q，想要 %q", lv, got, want)
		}
	}
}

// 结构化字段要拼进正文：控制台上那一栏是一行文本，
// 而「err=xxx」往往正是那条日志唯一有用的部分。
func TestAttrsAreFoldedIntoTheMessage(t *testing.T) {
	b := &LogBuffer{}
	log := slog.New(b.Handler(slog.NewTextHandler(discard{}, nil)))
	log.Error("应用配置失败", "cfg_version", "cfg-2f9a", "err", "unknown handler")

	lines := b.take()
	if len(lines) != 1 {
		t.Fatalf("应当有一条，实际 %d", len(lines))
	}
	msg := lines[0].GetMsg()
	for _, want := range []string{"应用配置失败", "cfg_version=cfg-2f9a", "err=unknown handler"} {
		if !contains(msg, want) {
			t.Errorf("正文里应当有 %q：%s", want, msg)
		}
	}
	if lines[0].GetLevel() != "error" {
		t.Errorf("level = %q", lines[0].GetLevel())
	}
}

// With() 带出来的字段也要进正文 —— 否则一个 log.With("node", id) 之后
// 所有日志都会丢掉那个 id，而那正是人最需要的定位信息。
func TestWithAttrsAreKept(t *testing.T) {
	b := &LogBuffer{}
	log := slog.New(b.Handler(slog.NewTextHandler(discard{}, nil))).With("node", "hk-01")
	log.Info("心跳")

	lines := b.take()
	if len(lines) != 1 || !contains(lines[0].GetMsg(), "node=hk-01") {
		t.Fatalf("With 的字段丢了：%+v", lines)
	}
}

// **满了丢最旧的。**
//
// 节点掉线时日志攒不出去，而那段时间里最该被送上来的是掉线之后发生了什么，
// 不是掉线之前的例行记录。
func TestOldestLinesAreDroppedWhenFull(t *testing.T) {
	b := &LogBuffer{}
	log := slog.New(b.Handler(slog.NewTextHandler(discard{}, nil)))
	for i := range logCapacity + 50 {
		log.Info("line", "i", i)
	}
	if got := b.len(); got != logCapacity {
		t.Fatalf("缓冲应当封顶在 %d，实际 %d", logCapacity, got)
	}
	first := b.take()[0].GetMsg()
	if contains(first, "i=0 ") || first == "line i=0" {
		t.Error("满了应当丢最旧的，而最旧的还在")
	}
}

// 送失败要放回队首，且保持时间顺序。
//
// 放回去可能让主控收到重复的一批，丢掉则让人永远看不到那几条。两者之间选重复：
// **一条重复的日志读得出来是重复的，一条缺失的日志读起来跟「那时什么也没发生」
// 一模一样。**
func TestPutBackKeepsOrder(t *testing.T) {
	b := &LogBuffer{}
	log := slog.New(b.Handler(slog.NewTextHandler(discard{}, nil)))
	log.Info("第一条")
	log.Info("第二条")

	batch := b.take()
	log.Info("第三条")
	b.putBack(batch)

	all := b.take()
	if len(all) != 3 {
		t.Fatalf("应当有三条，实际 %d", len(all))
	}
	if !contains(all[0].GetMsg(), "第一条") || !contains(all[2].GetMsg(), "第三条") {
		t.Fatalf("放回去之后顺序乱了：%v", []string{
			all[0].GetMsg(), all[1].GetMsg(), all[2].GetMsg()})
	}
}

// 一次取走有上限：隧道断在中途时只丢一批，其余还在缓冲里等下一次。
func TestTakeIsBounded(t *testing.T) {
	b := &LogBuffer{}
	log := slog.New(b.Handler(slog.NewTextHandler(discard{}, nil)))
	for range logBatchMax + 30 {
		log.Info("x")
	}
	if n := len(b.take()); n != logBatchMax {
		t.Fatalf("一批应当封顶在 %d，实际 %d", logBatchMax, n)
	}
	if b.len() != 30 {
		t.Fatalf("剩下的应当留在缓冲里，实际 %d", b.len())
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
