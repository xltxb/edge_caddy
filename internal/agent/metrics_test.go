package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
)

// **Agent 重启之后连接数不该归零。**
//
// 边缘端口原先只在收到一次下发时才设置，而重启后的 Agent 向主控报的就是
// 基线版本号 —— 主控看不到漂移，不会再推。于是从重启起到下一次人为下发，
// 每一台节点报上去的连接数都是 0，总览上「全网连接数」变成 0.0k，
// 而机器其实正扛着流量。灰度上三台节点同时降权重启之后就是这个样子。
//
// Caddy 自己知道它在哪些端口上监听，问它就行 —— 与 loadGeoDBFromDisk
// 同一条理由：重启不该把已经具备的能力丢掉。
func TestEdgePortsRecoveredFromRunningCaddyWithoutPush(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"apps":{"http":{"servers":{
			"edge":{"listen":[":80"]},
			"tls":{"listen":[":443"]}}}}}`))
	}))
	t.Cleanup(admin.Close)

	m := newMetricsCollector(NewCaddyClient(admin.URL))
	// 没有任何 setEdgePorts —— 模拟「重启后主控没有再推」。
	m.collect(context.Background())

	got := append([]uint32(nil), m.edgePorts...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if len(got) != 2 || got[0] != 80 || got[1] != 443 {
		t.Fatalf("边缘端口 = %v，想要 [80 443]：重启后没人推配置时连接数会一直是 0", got)
	}
}

// Caddy 还没起来（systemd 里 Agent 可能先于 Caddy 启动）时拿不到端口，
// 下一次采集要再试，而不是把一次失败当成「没有边缘端口」记住。
func TestEdgePortsRetriedUntilCaddyIsUp(t *testing.T) {
	var up bool
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"apps":{"http":{"servers":{"edge":{"listen":[":8443"]}}}}}`))
	}))
	t.Cleanup(admin.Close)

	m := newMetricsCollector(NewCaddyClient(admin.URL))
	m.collect(context.Background())
	if len(m.edgePorts) != 0 {
		t.Fatalf("Caddy 没起来时边缘端口 = %v，想要空", m.edgePorts)
	}

	up = true
	m.collect(context.Background())
	if len(m.edgePorts) != 1 || m.edgePorts[0] != 8443 {
		t.Fatalf("Caddy 起来之后边缘端口 = %v，想要 [8443]", m.edgePorts)
	}
}
