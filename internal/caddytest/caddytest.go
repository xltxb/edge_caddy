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
	"syscall"
	"testing"
	"time"
)

type Caddy struct {
	dir        string
	adminSock  string
	edgeSock   string
	edgeTCP    string // 非空时边缘走 TCP，remote_ip 才有东西可匹配
	tlsSock    string
	verifySock string
	t          *testing.T
	admin      *http.Client
}

// EdgeListen 是渲染时传给 render.Options.HTTPListen 的值。
// EdgeListen 是边缘 HTTP 的监听地址。
//
// 默认是 unix socket：并行测试之间不抢端口。**代价是 remote_ip 匹配不到任何东西**
// ——unix 连接没有来源 IP，于是 IP 白名单会拦下一切、黑名单谁也拦不到。
//
// 要验 IP 类规则就得用 TCP（EdgeTCP），那时来源是 127.0.0.1。
// 这个区别是实测出来的：白名单放行回环，走 unix socket 时回 404。
func (c *Caddy) EdgeListen() string {
	if c.edgeTCP != "" {
		return c.edgeTCP
	}
	return "unix/" + c.edgeSock
}

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

// EdgeTCP 让边缘 HTTP 监听在回环 TCP 上，而不是 unix socket。
//
// **只有这样 remote_ip 才有东西可匹配。** 默认不这么做是因为 TCP 要占端口，
// 而并行测试会抢；所以它是逐个测试选的，不是全局默认。
func EdgeTCP() Option { return func(c *Caddy) { c.edgeTCP = freePort(c.t) } }

// Option 是 New 的可选项。
type Option func(*Caddy)

// freePort 拿一个当前空闲的回环端口。
//
// **绑了再放**：中间有一个极小的窗口别人可能抢走它。测试里可以接受
// （症状是 Caddy 起不来，当场报错，不是静默的错），而要真正消除它
// 得把 fd 传给 Caddy —— 那要改它的启动方式，不成比例。
func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func New(t *testing.T, opts ...Option) *Caddy {
	t.Helper()

	bin, err := exec.LookPath("caddy")
	if err != nil {
		t.Fatalf("找不到 caddy 二进制：%v\n"+
			"渲染器的集成测试要求本机有钉死版本的 Caddy（2.11.x）。"+
			"这不与 ADR-0004 冲突——那条说的是「生产主控」不装 Caddy。", err)
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
	// 选项在这里应用：freePort 要用 c.t。
	for _, o := range opts {
		o(c)
	}

	home := filepath.Join(dir, "h")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}

	// 没有 apps 键 —— 刻意的，见包注释。
	cfgPath := filepath.Join(dir, "start.json")
	// **日志级别是 INFO，不是 ERROR。**
	//
	// 原先是 ERROR，于是那条「失败时打印 caddy 日志」的诊断
	// ——我上一轮**专门为一个 flake 加的**——从来没有输出过任何东西：
	// Caddy 的启动与重载信息都是 INFO 级，被过滤掉了。
	//
	// 写了一个诊断而没验证它诊断得出东西，跟写一句注释而没验证它说的是真的
	// 是同一个错。这次是在追那个 flake 的过程中撞见的：失败现场里没有
	// 「caddy 日志:」那一行，我一度把它当成线索（「Caddy 还没打印启动日志」），
	// 而真相是它**在成功时也是空的**。
	//
	// 一个恒为空的诊断比没有诊断更糟：它让人以为「那边没什么可说的」。
	cfg := fmt.Sprintf(
		`{"admin":{"listen":"unix/%s"},"logging":{"logs":{"default":{"level":"INFO"}}}}`,
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
		// **先看它是不是已经自己退出了，再去杀。**
		//
		// 全量并行跑时见过一次「POST /config/... : EOF」——admin socket 在
		// 响应之前被关掉了。那次没能复现，而当时最缺的一个事实正是
		// 「Caddy 是自己死了，还是只是那一次连接出了问题」。
		//
		// 这里不加重试来「修」它：重试会把这个信号永久掩埋，而 ADR-0005 的
		// 整套分类恰恰依赖「连不上」与「被拒绝」的区分。留下证据，
		// 下次复现时至少知道该往哪儿看。
		exitedOnItsOwn := cmd.ProcessState != nil
		if !exitedOnItsOwn {
			// Signal(0) 只探活、不真的发信号。
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				exitedOnItsOwn = true
			}
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()

		if t.Failed() {
			if exitedOnItsOwn {
				t.Logf("caddy 进程在测试结束前已经自己退出（state=%v）—— "+
					"这多半就是失败的原因", cmd.ProcessState)
			} else {
				t.Log("caddy 进程直到测试结束仍然活着 —— 失败不是因为它死了")
			}
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
func (c *Caddy) Client() *http.Client {
	if c.edgeTCP != "" {
		// **把任何 host 都拨到那个端口。**
		//
		// 不这么做的话客户端会真的去解析 `wl.example.com`，请求根本发不出去
		// ——而那表现成 code=0，看起来像「Caddy 没起来」。
		// Host 头仍然是原来的域名，Caddy 的 host 匹配器要的正是它。
		//
		// 守着这一整条（TCP 上 remote_ip 真的匹配得到回环）的是
		// TestIPBlacklistActuallyBlocks / TestIPBlacklistLetsOthersThrough
		// （internal/e2e）：它们用真 Caddy 发真请求，一条验拦、一条验放。
		addr := c.edgeTCP
		return &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
				},
			},
		}
	}
	return unixClient(c.edgeSock)
}

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

// unixClient 与 agent.NewCaddyClient 用同一套设置，**包括不复用连接**。
//
// 理由写在 internal/agent/caddy.go 的 NewCaddyClient 上（Caddy 每次写配置
// 都重启 admin 监听，池子里的连接必然作废）。这里跟着改，是因为
// **测试替身和被测对象在连接复用这件事上必须一致，否则测试跑的是另一条路径**
// ——那个 flake 恰恰是在这个包的测试里现身的。
//
// 守着这一致的是 TestTestDoubleDoesNotReuseConnectionsEither（client_test.go）。
// agent 那条测试管不到这里：把下面那行删掉，它照样全绿。
func unixClient(sock string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
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

// GetFull 与 Get 一样发请求，但把整个响应交回来——测试需要看响应头。
func (c *Caddy) GetFull(t *testing.T, host, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+host+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	cli := c.Client()
	cli.Timeout = 3 * time.Second
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("请求 %s%s: %v", host, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// Metrics 取 Caddy admin 上的 /metrics。
func (c *Caddy) Metrics(t *testing.T) (int, string) {
	t.Helper()
	resp, err := c.admin.Get("http://unix/metrics")
	if err != nil {
		t.Fatalf("取 metrics: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, string(b)
}
