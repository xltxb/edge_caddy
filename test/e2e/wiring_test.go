package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	edgemaster "github.com/xltxb/edge_caddy/internal/master"
)

// 主控进程的**装配**必须完整：每个接口都得拿到它依赖的组件。
//
// 这条测试是一个真实漏洞的反面。工单 #8 加了三个节点操作接口，单测和 e2e 全绿，
// 但 cmd/master 里的 api.Deps 从没填过 Nodes——真跑起来那三个接口一律返回
// 「节点通道未就绪」。测试之所以看不见，是因为每个测试自己装配 Deps，
// 装配这一步本身从来没有被任何测试走过。
//
// 判据是「503 未装配」不得出现：具体行为对不对由各自的测试管，
// 这里只守住「组件接上了没有」。
func TestMasterWiringLeavesNoComponentUnplugged(t *testing.T) {
	m, err := edgemaster.Assemble(context.Background(), edgemaster.Options{
		DBPath:   filepath.Join(t.TempDir(), "w.sqlite"),
		Hostname: "master.local",
		Secret:   []byte("test-master-key"),
	})
	if err != nil {
		t.Fatalf("装配主控失败: %v", err)
	}
	defer m.Close()

	// 未设口令，因此这些接口可直接访问（鉴权本身另有测试覆盖）
	cases := []struct {
		method, path string
		body         string
	}{
		{http.MethodGet, "/api/v1/overview", ""},
		{http.MethodGet, "/api/v1/nodes", ""},
		{http.MethodPost, "/api/v1/nodes/node-x/probe", ""},
		{http.MethodPost, "/api/v1/nodes/node-x/push", ""},
		{http.MethodPost, "/api/v1/nodes/node-x/drain", ""},
		{http.MethodGet, "/api/v1/routes", ""},
		{http.MethodGet, "/api/v1/deploys", ""},
		{http.MethodGet, "/api/v1/drafts", ""},
		{http.MethodGet, "/api/v1/rules", ""},
		{http.MethodGet, "/api/v1/audit", ""},
		{http.MethodGet, "/api/v1/alerts", ""},
		{http.MethodPut, "/api/v1/alerts", `{"enabled":false}`},
		{http.MethodPost, "/api/v1/alerts/test", ""},
		{http.MethodPost, "/api/v1/config/preview", `{}`},
	}
	for _, tc := range cases {
		var rdr *strings.Reader
		if tc.body != "" {
			rdr = strings.NewReader(tc.body)
		} else {
			rdr = strings.NewReader("")
		}
		req := httptest.NewRequest(tc.method, tc.path, rdr)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		m.HTTP.ServeHTTP(w, req)

		if w.Code == http.StatusServiceUnavailable {
			t.Errorf("%s %s 返回 503：某个组件没接上——%s", tc.method, tc.path, w.Body.String())
		}
		if w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "page not found") {
			t.Errorf("%s %s 没有注册路由", tc.method, tc.path)
		}
	}
}

// 装配后 gRPC 的两个服务都已注册。
//
// 漏注册的话，节点接入会拿到 Unimplemented，而主控日志里一切正常。
func TestMasterRegistersBothGRPCServices(t *testing.T) {
	m, err := edgemaster.Assemble(context.Background(), edgemaster.Options{
		DBPath:   filepath.Join(t.TempDir(), "w2.sqlite"),
		Hostname: "master.local",
		Secret:   []byte("test-master-key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	svcs := m.GRPC.GetServiceInfo()
	for _, want := range []string{"edge.v1.EdgeEnroll", "edge.v1.EdgeTunnel"} {
		if _, ok := svcs[want]; !ok {
			t.Errorf("gRPC 服务 %s 未注册——节点会拿到 Unimplemented，而主控日志里一切正常", want)
		}
	}
}

// 没有主密钥就拒绝启动。
//
// 「先明文存着以后再加密」的那个「以后」不会到来，而明文的 CA 私钥躺在库里
// 是不会有任何东西提醒你的。
func TestMasterRefusesToStartWithoutSecret(t *testing.T) {
	_, err := edgemaster.Assemble(context.Background(), edgemaster.Options{
		DBPath:   filepath.Join(t.TempDir(), "w3.sqlite"),
		Hostname: "master.local",
	})
	if err == nil {
		t.Fatal("没有主密钥必须拒绝启动")
	}
}
