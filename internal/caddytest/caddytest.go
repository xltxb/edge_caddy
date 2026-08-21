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
//
// # 为什么全部走 unix socket 而不是 TCP 端口
//
// 用「listen :0 拿端口再关掉」的办法分配端口有 TOCTOU 竞态：`go test ./...`
// 并行跑多个包时，两个 fixture 可能拿到同一个号。而在 macOS 上这**不会报错**
// ——ADR-0004 的复核实测过：重复 bind 会成功，产生一个收不到流量的幽灵监听，
// 请求进了另一个进程。表现出来就是「偶发失败、单独跑又好了」。
//
// unix socket 没有这个问题：路径由 mktemp 保证唯一，绑重了会直接失败而不是
// 静默错位。代价是 socket 路径受 macOS 的 sun_path 104 字节限制，
// 所以临时目录建在 /tmp 下用短名字，而不是 t.TempDir()。
package caddytest

import (
	"bytes"
	"context"
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
	dir        string
	adminSock  string
	edgeSock   string
	tlsSock    string
	verifySock string
	t          *testing.T
	admin      *http.Client
}

// EdgeListen 是渲染时传给 render.Options.HTTPListen 的值。
func (c *Caddy) EdgeListen() string { return "unix/" + c.edgeSock }

// AdminURL 是传给 agent.NewCaddyClient 的地址。
func (c *Caddy) AdminURL() string { return "unix/" + c.adminSock }

// TLSListen 是渲染时传给 render.Options.HTTPSListen 的值。
func (c *Caddy) TLSListen() string { return "unix/" + c.tlsSock }

// TLSSocketPath 是 :443 那台 server 的 socket 路径，供测试直接握手。
func (c *Caddy) TLSSocketPath() string { return c.tlsSock }

// VerifyDial 是渲染时传给 render.Options.VerifyAddr 的值，
// 指向 Agent 校验端点在本机的 socket。
func (c *Caddy) VerifyDial() string { return "unix/" + c.verifySock }

// VerifySocketPath 是校验端点应当监听的 socket 路径。
func (c *Caddy) VerifySocketPath() string { return c.verifySock }

func New(t *testing.T) *Caddy {
	t.Helper()

	bin, err := exec.LookPath("caddy")
	if err != nil {
		t.Fatalf("找不到 caddy 二进制：%v\n"+
			"渲染器的集成测试要求本机有钉死版本的 Caddy（2.11.x）。"+
			"这不与 ADR-0004 冲突——那条说的是**生产主控**不装 Caddy。", err)
	}

	// 短路径：macOS 的 sun_path 只有 104 字节，t.TempDir() 给的路径远超它。
	dir, err := os.MkdirTemp("/tmp", "ecT.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	c := &Caddy{
		dir:        dir,
		adminSock:  filepath.Join(dir, "a.sock"),
		edgeSock:   filepath.Join(dir, "e.sock"),
		tlsSock:    filepath.Join(dir, "s.sock"),
		verifySock: filepath.Join(dir, "v.sock"),
		t:          t,
	}

	home := filepath.Join(dir, "h")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}

	// 没有 apps 键 —— 刻意的，见包注释。
	cfgPath := filepath.Join(dir, "start.json")
	cfg := fmt.Sprintf(
		`{"admin":{"listen":"unix/%s"},"logging":{"logs":{"default":{"level":"ERROR"}}}}`,
		c.adminSock)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(dir, "caddy.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "run", "--config", cfgPath)
	cmd.Env = append(os.Environ(),
		"HOME="+home, "XDG_DATA_HOME="+home, "XDG_CONFIG_HOME="+home)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 caddy: %v", err)
	}

	c.admin = unixClient(c.adminSock)

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

	c.waitReady()
	return c
}

// Client 返回一个打边缘 server 的 HTTP 客户端。
// 用它发请求时 URL 里的主机名只用于 Host 匹配，实际连的是 socket。
func (c *Caddy) Client() *http.Client { return unixClient(c.edgeSock) }

// Get 经边缘 server 发一个请求，host 决定命中哪条路由。
func (c *Caddy) Get(host, path string, headers map[string]string) (int, string) {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+host+path, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	cli := c.Client()
	cli.Timeout = 3 * time.Second
	resp, err := cli.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, string(b)
}

// Config 读回 Caddy 当前的完整配置。
func (c *Caddy) Config() map[string]any {
	c.t.Helper()
	resp, err := c.admin.Get("http://caddy-admin/config/")
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
	resp, err := c.admin.Post("http://caddy-admin/config/apps/"+name,
		"application/json", bytes.NewReader(body))
	if err != nil {
		c.t.Fatalf("POST /config/apps/%s: %v", name, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func unixClient(sock string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
	}
}

func (c *Caddy) waitReady() {
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := c.admin.Get("http://caddy-admin/config/")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if b, err := os.ReadFile(filepath.Join(c.dir, "caddy.log")); err == nil {
		c.t.Logf("caddy 日志:\n%s", b)
	}
	c.t.Fatal("caddy admin 在 10 秒内没有就绪")
}
