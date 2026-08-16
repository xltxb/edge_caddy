package agent_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/xltxb/edge_caddy/internal/agent"
)

// fakeCaddy 复刻 Caddy Admin API 实测出来的路径语义：
//
//	POST /config/apps        → 用载荷**整体替换** apps
//	POST /config/apps/<name> → 只替换该 app，其余原样保留
//
// 复刻这一层是必要的：这两条路径的差别正是被测行为本身，
// 用一个「收到什么都返回 200」的假服务端，测了等于没测。
type fakeCaddy struct {
	mu     sync.Mutex
	apps   map[string]json.RawMessage
	paths  []string
	status int // 非 0 时用它作为响应码
	body   string
}

func newFakeCaddy(seed map[string]json.RawMessage) (*fakeCaddy, *httptest.Server) {
	f := &fakeCaddy{apps: map[string]json.RawMessage{}}
	for k, v := range seed {
		f.apps[k] = v
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blob, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)

		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.body))
			return
		}
		switch {
		case r.URL.Path == "/config/apps":
			var repl map[string]json.RawMessage
			if err := json.Unmarshal(blob, &repl); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			f.apps = repl
		case strings.HasPrefix(r.URL.Path, "/config/apps/"):
			f.apps[strings.TrimPrefix(r.URL.Path, "/config/apps/")] = json.RawMessage(blob)
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	return f, srv
}

func (f *fakeCaddy) appNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.apps))
	for k := range f.apps {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (f *fakeCaddy) requestPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

// 下发配置不得抹掉节点上其他 app。
//
// 上一版就是在这里出的事：整体 POST /config/apps 会把外部证书平台写入的
// tls app 一并替换掉，于是**每一次发布**都让节点上所有 HTTPS 站点失去证书，
// 而面板显示的是一次成功发布——零反馈。
//
// 断言的是「tls 还在」这个后果，不是 URL 字符串：换个等效写法也应当通过。
func TestApplyPreservesOtherApps(t *testing.T) {
	fake, srv := newFakeCaddy(map[string]json.RawMessage{
		"tls": json.RawMessage(`{"certificates":{"load_files":[{"certificate":"/etc/ssl/site.crt"}]}}`),
	})
	defer srv.Close()

	c := agent.NewCaddyClient(srv.URL)
	if _, err := c.Apply(context.Background(),
		[]byte(`{"http":{"servers":{"edge":{"listen":[":443"]}}}}`)); err != nil {
		t.Fatalf("下发失败: %v", err)
	}

	names := fake.appNames()
	if !contains(names, "tls") {
		t.Fatalf("下发后 tls app 被抹掉了——节点上所有证书失效；现存: %v", names)
	}
	if !contains(names, "http") {
		t.Fatalf("下发后 http app 不存在，配置没生效；现存: %v", names)
	}
}

// 载荷里有多个 app 时，每个都要下发到自己的路径。
func TestApplySendsEachAppSeparately(t *testing.T) {
	fake, srv := newFakeCaddy(nil)
	defer srv.Close()

	c := agent.NewCaddyClient(srv.URL)
	if _, err := c.Apply(context.Background(),
		[]byte(`{"http":{"servers":{}},"tls":{"certificates":{}}}`)); err != nil {
		t.Fatalf("下发失败: %v", err)
	}
	paths := fake.requestPaths()
	for _, want := range []string{"POST /config/apps/http", "POST /config/apps/tls"} {
		if !contains(paths, want) {
			t.Errorf("缺少请求 %q，实际 %v", want, paths)
		}
	}
	if contains(paths, "POST /config/apps") {
		t.Errorf("不应整体 POST /config/apps——那会抹掉未包含在载荷里的 app：%v", paths)
	}
}

// Caddy 拒绝配置时，失败原文必须原样带回去。
//
// 实测 Caddy 对语法错误、未知 handler、字段类型错、端口占用一律返回 500，
// 状态码分不出该不该重试（ADR-0005）。因此这里不做任何归类，
// 把原文交给上层——排查时唯一有用的就是那段原文。
func TestApplySurfacesCaddyErrorVerbatim(t *testing.T) {
	fake, srv := newFakeCaddy(nil)
	defer srv.Close()
	fake.status = http.StatusInternalServerError
	fake.body = `{"error":"loading http app module: unknown handler nope_xyz"}`

	c := agent.NewCaddyClient(srv.URL)
	_, err := c.Apply(context.Background(), []byte(`{"http":{}}`))
	if err == nil {
		t.Fatal("Caddy 拒绝时应返回错误")
	}
	if !strings.Contains(err.Error(), "unknown handler nope_xyz") {
		t.Fatalf("错误应包含 Caddy 的原文，实际: %v", err)
	}
}

// 空载荷必须被拒绝，不能当成「下发一份空配置」。
//
// 空配置会把节点上正在跑的服务全部清掉——一次误操作变成一次全站中断。
func TestApplyRejectsEmptyPayload(t *testing.T) {
	_, srv := newFakeCaddy(nil)
	defer srv.Close()

	c := agent.NewCaddyClient(srv.URL)
	for name, payload := range map[string]string{
		"空对象":     `{}`,
		"空字节":     ``,
		"非法 JSON": `{`,
	} {
		if _, err := c.Apply(context.Background(), []byte(payload)); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
