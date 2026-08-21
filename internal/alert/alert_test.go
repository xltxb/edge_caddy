package alert_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xltxb/edge_caddy/internal/alert"
	"github.com/xltxb/edge_caddy/internal/secret"
	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
)

type recorder struct {
	mu     sync.Mutex
	bodies []string
	fail   int // 前 fail 次返回 500
	hits   int
}

func (r *recorder) server(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.hits++
		n := r.hits
		r.bodies = append(r.bodies, string(b))
		r.mu.Unlock()
		if n <= r.fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"msg":"down"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (r *recorder) snapshot() (int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits, append([]string(nil), r.bodies...)
}

func setup(t *testing.T, level string, webhook, lark string, atAll bool) (*alert.Notifier, *store.Store) {
	t.Helper()
	st := testdb.New(t)
	sealer, err := secret.New([]byte("alert-test-master-key-32-bytes!!!"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutAlertSettings(context.Background(), store.AlertSettings{
		NotifyLevel: level, WebhookURL: webhook, LarkWebhook: lark, AtAllOnCrit: atAll,
	}, sealer); err != nil {
		t.Fatal(err)
	}
	return alert.New(st, sealer, nil), st
}

// 级别过滤：notify_level 决定什么值得打扰人。
func TestNotifyLevelFiltering(t *testing.T) {
	cases := []struct {
		level string
		send  string
		want  bool
	}{
		{"crit", "warn", false},
		{"crit", "crit", true},
		{"warn", "info", false},
		{"warn", "warn", true},
		{"warn", "crit", true},
		{"all", "info", true},
		{"all", "ok", true},
	}
	for _, c := range cases {
		t.Run(c.level+"/"+c.send, func(t *testing.T) {
			r := &recorder{}
			n, _ := setup(t, c.level, r.server(t), "", false)
			n.Notify(context.Background(), c.send, "标题", "正文")
			hits, _ := r.snapshot()
			if (hits > 0) != c.want {
				t.Fatalf("notify_level=%s 收到 %s：投递了 %d 次，想要 want=%v", c.level, c.send, hits, c.want)
			}
		})
	}
}

// Lark 卡片：crit 用红色模板，开了 at_all 时附 <at id=all></at>。
func TestLarkCardShapeAndAtAll(t *testing.T) {
	r := &recorder{}
	n, _ := setup(t, "warn", "", r.server(t), true)
	n.Notify(context.Background(), "crit", "节点离线 node-us-01", "心跳连续超时 3 次")

	_, bodies := r.snapshot()
	if len(bodies) != 1 {
		t.Fatalf("投递次数 = %d", len(bodies))
	}
	var card struct {
		MsgType string `json:"msg_type"`
		Card    struct {
			Header struct {
				Template string `json:"template"`
				Title    struct {
					Content string `json:"content"`
				} `json:"title"`
			} `json:"header"`
			Elements []struct {
				Text struct {
					Content string `json:"content"`
				} `json:"text"`
			} `json:"elements"`
		} `json:"card"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &card); err != nil {
		t.Fatalf("不是合法的卡片: %v\n%s", err, bodies[0])
	}
	if card.MsgType != "interactive" {
		t.Errorf("msg_type = %q", card.MsgType)
	}
	if card.Card.Header.Template != "red" {
		t.Errorf("crit 应当用红色模板，实际 %q", card.Card.Header.Template)
	}
	if !strings.Contains(card.Card.Elements[0].Text.Content, "<at id=all></at>") {
		t.Errorf("crit 且开了 at_all 时应当附 @所有人：%q", card.Card.Elements[0].Text.Content)
	}
}

// at_all 只在 crit 时生效——warn 也 @所有人会让这个开关很快被关掉。
func TestAtAllOnlyAppliesToCrit(t *testing.T) {
	r := &recorder{}
	n, _ := setup(t, "warn", "", r.server(t), true)
	n.Notify(context.Background(), "warn", "标题", "正文")
	_, bodies := r.snapshot()
	if len(bodies) != 1 {
		t.Fatalf("投递次数 = %d", len(bodies))
	}
	if strings.Contains(bodies[0], "<at id=all></at>") {
		t.Fatal("warn 级别不该 @所有人")
	}
}

// Webhook 失败重试 3 次（后端文档 §7）。
func TestWebhookRetriesThreeTimes(t *testing.T) {
	r := &recorder{fail: 99} // 一直失败
	n, st := setup(t, "warn", r.server(t), "", false)
	n.Notify(context.Background(), "crit", "标题", "正文")

	hits, _ := r.snapshot()
	if hits != 3 {
		t.Fatalf("投递尝试 = %d 次，想要 3", hits)
	}

	// 失败必须留痕：告警静默丢了没人会发现。
	entries, err := st.ListAudit(context.Background(), "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 || entries[0].Action != "发送告警" || entries[0].Result != "fail" {
		t.Fatalf("审计 = %+v，想要一条失败的「发送告警」", entries)
	}
	// 下游原文是排查 webhook 配错的唯一线索。
	if !strings.Contains(entries[0].Detail, "500") {
		t.Errorf("审计 detail 应当带上下游的原文：%q", entries[0].Detail)
	}
}

// 前两次失败、第三次成功 —— 重试真的起作用。
func TestWebhookSucceedsOnRetry(t *testing.T) {
	r := &recorder{fail: 2}
	n, st := setup(t, "warn", r.server(t), "", false)
	n.Notify(context.Background(), "crit", "标题", "正文")

	hits, _ := r.snapshot()
	if hits != 3 {
		t.Fatalf("投递尝试 = %d 次，想要 3（前两次失败）", hits)
	}
	entries, _ := st.ListAudit(context.Background(), "", 10, 0)
	if len(entries) == 0 || entries[0].Result != "ok" {
		t.Fatalf("第三次成功应当记 ok，实际 %+v", entries)
	}
}

// 一条渠道都没配时不留痕 —— 没发生的事不该在审计里占一行。
func TestNoChannelsConfiguredWritesNoAudit(t *testing.T) {
	n, st := setup(t, "all", "", "", false)
	n.Notify(context.Background(), "crit", "标题", "正文")
	entries, _ := st.ListAudit(context.Background(), "", 10, 0)
	for _, e := range entries {
		if e.Action == "发送告警" {
			t.Fatalf("没有渠道时不该写告警审计: %+v", e)
		}
	}
}
