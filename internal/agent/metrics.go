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

		// **被访问规则拦下的请求数。**
		//
		// 判据是 `handler="static_response"` —— 那个 handler **只有 blockHandler
		// 在用**（render.go 里三处，全是拦截）。所以它出现一次就是我们拦了一次，
		// 与状态码无关。
		//
		// # 为什么不按状态码算
		//
		// 第一版是「code 是 403/404/429 就算被拦」，那有两个洞：
		//
		//  一、**上游自己返回的 403/404 也被算进来** —— 一个正常网站有大量 404，
		//      这个数会被噪音淹没。而它要回答的是「此刻在不在被打」，
		//      一个平时就很大的数回答不了那个问题。
		//
		//  二、口径取的是 `handler="headers"`（为了不重复计数），而**那个 handler
		//      在关掉响应头处理时整个不存在** —— 计数会恒为 0，无声。
		//
		// 限流的 429 不在这里数：它是校验端点回的，经 reverse_proxy 透传，
		// 而 reverse_proxy 也处理正常回源 —— 分不出「我们限的」和「上游限的」。
		// 那一半由 Agent 自己的计数器数（VerifyServer.RateLimited），精确。
		//
		// **`abort` 那一档两边都数不到**：它静默断连，不产生响应，
		// Caddy 的按状态码计数里一条都没有。实测确认过。
		if strings.HasPrefix(line, "caddy_http_request_duration_seconds_count{") &&
			strings.Contains(line, `handler="static_response"`) {
			if v, ok := metricValue(line); ok {
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
