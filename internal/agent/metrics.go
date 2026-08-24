package agent

import (
	"bufio"
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// Metrics 是一次心跳要上报的观测量。
//
// 拿不到的一律留零并在上层按「没有数据」处理，**不编数字**：
// 一个编出来的负载值会让人据此做判断，而它什么也不代表。
type Metrics struct {
	CPU         float64
	Mem         float64
	Conns       uint32
	ReqTotal    uint64
	OriginTotal uint64
	// BlockedTotal 是被访问规则拦下的请求数（429 / 403 / 404）。
	// **abort 那一档数不到** —— 它静默断连，不产生响应。
	BlockedTotal uint64
	Routes       uint32
	Rules        uint32
}

// metricsCollector 采集本机与本机 Caddy 的观测量。
type metricsCollector struct {
	caddy     *CaddyClient
	edgePorts []uint32
}

func newMetricsCollector(c *CaddyClient) *metricsCollector {
	return &metricsCollector{caddy: c}
}

// setEdgePorts 告诉采集器边缘 server 监听在哪些端口——连接数只统计它们上面的。
// 不限定的话会把 SSH、隧道自己的连接都算进去，那个数字就没有意义了。
func (m *metricsCollector) setEdgePorts(ports []uint32) { m.edgePorts = ports }

func (m *metricsCollector) collect(ctx context.Context) Metrics {
	var out Metrics

	// CPU 取一小段采样窗口。传 0 会返回自进程启动以来的平均值——
	// 那个数字在长跑的进程里几乎不动，看起来像卡住了。
	if pcts, err := cpu.PercentWithContext(ctx, 200*time.Millisecond, false); err == nil && len(pcts) > 0 {
		out.CPU = round1(pcts[0])
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		out.Mem = round1(vm.UsedPercent)
	}
	out.Conns = m.countConns(ctx)

	req, origin, blocked, ok := m.scrapeCaddy(ctx)
	if ok {
		out.ReqTotal, out.OriginTotal, out.BlockedTotal = req, origin, blocked
	}
	return out
}

func (m *metricsCollector) countConns(ctx context.Context) uint32 {
	if len(m.edgePorts) == 0 {
		return 0
	}
	conns, err := net.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		return 0
	}
	want := map[uint32]bool{}
	for _, p := range m.edgePorts {
		want[p] = true
	}
	var n uint32
	for _, c := range conns {
		if c.Status == "ESTABLISHED" && want[c.Laddr.Port] {
			n++
		}
	}
	return n
}

// scrapeCaddy 从本机 Caddy 的 Admin /metrics 读请求计数。
//
// caddy_http_requests_total{handler="reverse_proxy"} 是**到达 upstream** 的请求数，
// 其余 handler 的是被访问规则拦下或由静态响应处理掉的。回源率就是前者除以总和
// （api-contract §3）。
//
// 注意这不是缓存命中率：官方 Caddy 没有缓存模块。
func (m *metricsCollector) scrapeCaddy(ctx context.Context) (req, origin, blocked uint64, ok bool) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, m.caddy.Admin+"/metrics", nil)
	if err != nil {
		return 0, 0, 0, false
	}
	resp, err := m.caddy.HTTP.Do(r)
	if err != nil {
		return 0, 0, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, 0, false
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()

		// **被拦下的请求数，按状态码算。**
		//
		// Caddy 自己就在按 code 计数（caddy_http_request_duration_seconds_count），
		// 所以不必在 Agent 里另加一个计数器 —— 另加的那个只数得到走校验端点的
		// 那几种，而 IP 黑名单和请求特征是 Caddy 原生匹配器拦的，它看不见。
		//
		// **指标名与标签是实测出来的，不是查文档来的**：真 Caddy 打了 6 个请求
		// （桶容量 2），拿到的正是
		// `caddy_http_request_duration_seconds_count{code="429",handler="headers",…} 4`。
		// 第一版我写成了 `caddy_http_response_…`（response 而不是 request），
		// **前缀匹配不上，函数照常返回 0，没有任何东西报错**。
		//
		// 守着这一条的是 TestBlockedCountReflectsRealBlocks（internal/e2e）：
		// 它用真 Caddy 打出真的 429，再看这个数涨没涨。指标名改错、
		// 或者 Caddy 哪天换了名字，那条会红。
		//
		// **只取 handler="headers" 那一行。** 同一个请求会在链上每个 handler
		// 各记一次（headers、static_response、reverse_proxy…），求和会重复计数。
		// 而响应头处理挂在最前面，每个请求必经 —— 拿它当口径，一个请求一次。
		if strings.HasPrefix(line, "caddy_http_request_duration_seconds_count{") &&
			strings.Contains(line, `handler="headers"`) {
			if v, ok := metricValue(line); ok && isBlockedCode(line) {
				blocked += uint64(v)
			}
			continue
		}

		if !strings.HasPrefix(line, "caddy_http_requests_total{") {
			continue
		}
		labels, value, found := strings.Cut(line, "} ")
		if !found {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			continue
		}
		req += uint64(v)
		if strings.Contains(labels, `handler="reverse_proxy"`) {
			origin += uint64(v)
		}
	}
	return req, origin, blocked, true
}

// isBlockedCode 说这一行的状态码算不算「被我们拦下」。
//
//	429  限流
//	403  访问规则拒绝（block_mode 是 403 时），或校验端点验不过
//	404  访问规则拒绝（block_mode 是 404 时）
//
// **`abort` 那一档数不到。** 它静默断连，不产生任何响应 —— 实测过：
// abort 模式下这些指标里一条带 code 的记录都没有。
// 那不是这段代码的缺陷，是那个模式本身的形态：**它对外不可区分于网络故障**，
// 对内也一样。要看得见拦了多少，路由的处置方式得是 403 或 404。
func isBlockedCode(line string) bool {
	for _, c := range []string{`code="429"`, `code="403"`, `code="404"`} {
		if strings.Contains(line, c) {
			return true
		}
	}
	return false
}

func metricValue(line string) (float64, bool) {
	_, value, found := strings.Cut(line, "} ")
	if !found {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return v, err == nil
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
