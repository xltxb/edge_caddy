// Package caddytest 给测试起一个真 Caddy。
//
// 它的形状与 internal/testdb 一致，存在的理由也一样：有些事实只有真的那个东西
// 能回答。对 Caddy 来说是两条，而且**两条都真的咬过人**：
//
//   - Caddy 对 JSON 语法错、未知 handler、字段类型错一律返回 500，
//     所以「按状态码分类重试」这条路走不通（ADR-0005 的由来）。
//   - POST /config/apps/<name> 而 config 里没有 apps 键会 500——而那正是一台
//     刚装完官方包、Caddyfile 为空的机器的状态（ADR-0010 的由来）。
//
// 因此本 fixture **刻意用一个没有 apps 键的配置起 Caddy**。上一版的测试装置
// 一直用 {"apps":{}}，把第二条整个盖住了，直到在真机上才炸出来。
package caddytest

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type Caddy struct {
	AdminAddr string // host:port
	HTTPPort  int    // 分配给边缘 server 的端口，渲染时用它替掉 :80
	logPath   string
	t         *testing.T
}

func (c *Caddy) AdminURL() string { return "http://" + c.AdminAddr }

// New 起一个 Caddy，测试结束时关掉。
func New(t *testing.T) *Caddy {
	t.Helper()

	bin, err := exec.LookPath("caddy")
	if err != nil {
		t.Fatalf("找不到 caddy 二进制：%v\n"+
			"渲染器的集成测试要求本机有钉死版本的 Caddy（2.11.x）。"+
			"这不与 ADR-0004 冲突——那条说的是**生产主控**不装 Caddy。", err)
	}

	adminPort := freePort(t)
	httpPort := freePort(t)
	dir := t.TempDir()

	// 没有 apps 键 —— 刻意的，见包注释。
	cfgPath := filepath.Join(dir, "start.json")
	cfg := fmt.Sprintf(`{"admin":{"listen":"127.0.0.1:%d"},"logging":{"logs":{"default":{"level":"ERROR"}}}}`, adminPort)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(dir, "caddy.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "run", "--config", cfgPath)
	// Caddy 会往数据目录写状态；指到临时目录，避免测试之间互相看见对方的证书缓存。
	cmd.Env = append(os.Environ(),
		"HOME="+dir, "XDG_DATA_HOME="+dir, "XDG_CONFIG_HOME="+dir)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 caddy: %v", err)
	}

	c := &Caddy{
		AdminAddr: fmt.Sprintf("127.0.0.1:%d", adminPort),
		HTTPPort:  httpPort,
		logPath:   logPath,
		t:         t,
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if t.Failed() {
			if b, err := os.ReadFile(logPath); err == nil && len(b) > 0 {
				t.Logf("caddy 日志:\n%s", b)
			}
		}
		_ = logFile.Close()
	})

	waitReady(t, c.AdminURL()+"/config/")
	return c
}

// Config 读回 Caddy 当前的完整配置。
func (c *Caddy) Config() map[string]any {
	c.t.Helper()
	resp, err := http.Get(c.AdminURL() + "/config/")
	if err != nil {
		c.t.Fatalf("读 caddy 配置: %v", err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		c.t.Fatalf("解析 caddy 配置: %v", err)
	}
	return m
}

// PostApp 直接 POST 一个 app，不做任何补救——用来观察 Caddy 的原始行为。
func (c *Caddy) PostApp(name string, body []byte) (int, string) {
	c.t.Helper()
	resp, err := http.Post(c.AdminURL()+"/config/apps/"+name, "application/json", bytesReader(body))
	if err != nil {
		c.t.Fatalf("POST /config/apps/%s: %v", name, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func bytesReader(b []byte) *readerAt { return &readerAt{b: b} }

type readerAt struct {
	b []byte
	i int
}

func (r *readerAt) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
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

func waitReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("caddy admin 在 10 秒内没有就绪")
}
