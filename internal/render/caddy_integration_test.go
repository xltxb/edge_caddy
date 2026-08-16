package render_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
)

// TestRenderedConfigRunsOnRealCaddy 把渲染产物喂给真实 Caddy 并跑真实流量。
//
// 单测只能证明产物「长得像我们想要的样子」，证明不了 Caddy 接受它。项目的
// 既定规则是「需求听文档，机制听实测」，而渲染器是下发的唯一权威——它产出
// 一份 Caddy 拒绝的配置，等于每个节点都下发失败。这条测试是那个断言。
//
// 需要 caddy 二进制：设 EDGE_TEST_CADDY 指向它，或让它在 PATH 里。
// 找不到时 SKIP 并明确说明「该路径未被覆盖」——静默跳过会让人以为跑过了。
func TestRenderedConfigRunsOnRealCaddy(t *testing.T) {
	bin := caddyBin(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "UPSTREAM host=%s xfp=%s", r.Host, r.Header.Get("X-Forwarded-Proto"))
	}))
	defer upstream.Close()

	edgePort, adminPort := freePort(t), freePort(t)
	routes := []model.Route{
		{
			Domain: "guarded.example.com", Upstream: hostPort(upstream.URL),
			Block: model.Block403, BodyMax: "5MB", Compress: true,
			Whitelist: []string{"198.51.100.7"}, // 故意不含 127.0.0.1
		},
		{
			Domain: "open.example.com", Upstream: hostPort(upstream.URL),
			Block: model.BlockAbort, BodyMax: "1MB",
			Whitelist: []string{model.AllowAllCIDR},
		},
	}
	apps, err := render.Caddy(routes)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	// 与 Agent 的下发方式一致：只替换 http 这一个 app。测试里直接以整份
	// 配置启动，但监听端口要改成非特权端口。
	var tree map[string]any
	if err := json.Unmarshal(apps, &tree); err != nil {
		t.Fatal(err)
	}
	srv := tree["http"].(map[string]any)["servers"].(map[string]any)["edge"].(map[string]any)
	srv["listen"] = []string{fmt.Sprintf("127.0.0.1:%d", edgePort)}

	full := map[string]any{
		"admin": map[string]any{"listen": fmt.Sprintf("127.0.0.1:%d", adminPort)},
		"apps":  tree,
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "caddy.json")
	blob, _ := json.Marshal(full)
	if err := os.WriteFile(cfgPath, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "run", "--config", cfgPath)
	logs := &syncBuf{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 caddy: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	adminURL := fmt.Sprintf("http://127.0.0.1:%d/config/", adminPort)
	if !waitFor(3*time.Second, func() bool {
		resp, err := http.Get(adminURL)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}) {
		t.Fatalf("caddy 未能加载渲染产物——说明渲染器产出了 Caddy 不接受的配置。\n日志:\n%s", logs.String())
	}

	edge := fmt.Sprintf("http://127.0.0.1:%d/", edgePort)
	get := func(host string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, edge, nil)
		req.Host = host
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// 白名单不含 127.0.0.1，处置方式为 403
	if code, _ := get("guarded.example.com"); code != http.StatusForbidden {
		t.Errorf("非白名单来源应被 403，实际 %d", code)
	}
	// 0.0.0.0/0 等于放行，且必须真的回源
	code, body := get("open.example.com")
	if code != http.StatusOK {
		t.Errorf("放行域名应 200，实际 %d（%s）", code, body)
	}
	if want := "UPSTREAM host="; len(body) < len(want) || body[:len(want)] != want {
		t.Errorf("请求没有真正回源，响应体: %q", body)
	}
}

func caddyBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("EDGE_TEST_CADDY"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		t.Fatalf("EDGE_TEST_CADDY=%s 指向的文件不存在", p)
	}
	if p, err := exec.LookPath("caddy"); err == nil {
		return p
	}
	t.Skip("未找到 caddy 二进制，「渲染产物能否被真实 Caddy 接受」这条路径未被覆盖；" +
		"设 EDGE_TEST_CADDY 指向 caddy 后重跑")
	return ""
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func hostPort(rawURL string) string {
	return rawURL[len("http://"):]
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

type syncBuf struct {
	mu  chanMutex
	buf []byte
}

type chanMutex chan struct{}

func (c *chanMutex) lock() {
	if *c == nil {
		*c = make(chanMutex, 1)
	}
	*c <- struct{}{}
}
func (c *chanMutex) unlock() { <-*c }

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.lock()
	defer s.mu.unlock()
	s.buf = append(s.buf, p...)
	return len(p), nil
}

func (s *syncBuf) String() string {
	s.mu.lock()
	defer s.mu.unlock()
	return string(s.buf)
}
