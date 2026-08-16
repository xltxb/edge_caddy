package dnsprovider_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xltxb/edge_caddy/internal/dnsprovider"
)

// cfServer 是一个按 Cloudflare API v4 真实形状应答的服务端。
//
// 用真服务端而不是打桩 http.Client：这一层唯一的产出就是「发出去的请求长什么样、
// 回来的 JSON 怎么解」，打桩会把它整个跳过。响应体照抄官方文档的字段结构，
// 不是我顺手编的形状——编出来的形状只能证明我的解析器能解我自己编的东西。
type cfServer struct {
	srv *httptest.Server

	mu      sync.Mutex
	reqs    []cfReq
	records map[string]cfRecord // id -> record
	nextID  int
	// zoneErr 非空时，查 zone 返回这个错误码
	zoneErr int
}

type cfReq struct {
	method string
	path   string
	query  string
	auth   string
	body   map[string]any
}

type cfRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
}

func newCFServer(t *testing.T) *cfServer {
	t.Helper()
	s := &cfServer{records: map[string]cfRecord{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blob, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var body map[string]any
		_ = json.Unmarshal(blob, &body)

		s.mu.Lock()
		s.reqs = append(s.reqs, cfReq{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			auth: r.Header.Get("Authorization"), body: body,
		})
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			s.mu.Lock()
			code := s.zoneErr
			s.mu.Unlock()
			if code != 0 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Unauthorized to access requested resource"}],"result":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[{"id":"023e105f4ecef8ad9ca31a8372d0c353","name":"example.com","status":"active"}],"result_info":{"count":1,"total_count":1}}`))

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dns_records"):
			s.mu.Lock()
			s.nextID++
			rec := cfRecord{
				ID:      "rec-" + string(rune('a'+s.nextID-1)),
				Type:    str(body["type"]),
				Name:    str(body["name"]),
				Content: str(body["content"]),
			}
			s.records[rec.ID] = rec
			s.mu.Unlock()
			out, _ := json.Marshal(map[string]any{"success": true, "errors": []any{}, "result": rec})
			_, _ = w.Write(out)

		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dns_records"):
			name := r.URL.Query().Get("name")
			content := r.URL.Query().Get("content")
			var hits []cfRecord
			s.mu.Lock()
			for _, rec := range s.records {
				if rec.Name == name && (content == "" || rec.Content == content) {
					hits = append(hits, rec)
				}
			}
			s.mu.Unlock()
			out, _ := json.Marshal(map[string]any{"success": true, "errors": []any{}, "result": hits})
			_, _ = w.Write(out)

		case r.Method == http.MethodDelete:
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			s.mu.Lock()
			delete(s.records, id)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"id":"` + id + `"}}`))

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":7003,"message":"Could not route to path"}]}`))
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func (s *cfServer) requests() []cfReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]cfReq, len(s.reqs))
	copy(out, s.reqs)
	return out
}

func (s *cfServer) recordCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func newCF(t *testing.T, s *cfServer) *dnsprovider.Cloudflare {
	t.Helper()
	return dnsprovider.NewCloudflare(dnsprovider.CloudflareConfig{
		APIToken: "cf-token-secret",
		BaseURL:  s.srv.URL,
	})
}

// 写一条 TXT 记录：先查 zone，再建记录，凭据走 Bearer 头。
func TestCloudflareSetTXT(t *testing.T) {
	s := newCFServer(t)
	cf := newCF(t, s)

	if err := cf.SetTXT(context.Background(), "_acme-challenge.example.com", "token-value-1"); err != nil {
		t.Fatal(err)
	}

	reqs := s.requests()
	if len(reqs) < 2 {
		t.Fatalf("应先查 zone 再建记录，实际只发了 %d 个请求", len(reqs))
	}
	// 凭据必须在 Authorization 头里，不能进 URL——URL 会进代理日志、进浏览器历史
	for _, r := range reqs {
		if r.auth != "Bearer cf-token-secret" {
			t.Errorf("%s %s 缺少 Bearer 凭据，实际 %q", r.method, r.path, r.auth)
		}
		if strings.Contains(r.query, "cf-token-secret") {
			t.Errorf("凭据出现在 URL 查询串里：%s", r.query)
		}
	}
	create := reqs[len(reqs)-1]
	if create.method != http.MethodPost {
		t.Fatalf("最后一步应是创建记录，实际 %s", create.method)
	}
	if got := str(create.body["type"]); got != "TXT" {
		t.Errorf("记录类型应为 TXT，实际 %q", got)
	}
	if got := str(create.body["name"]); got != "_acme-challenge.example.com" {
		t.Errorf("记录名不对，实际 %q", got)
	}
	if got := str(create.body["content"]); got != "token-value-1" {
		t.Errorf("记录值不对，实际 %q", got)
	}
}

// 删除 TXT 时**按值精确匹配**，不能把同名的其它挑战记录一起删掉。
//
// 同时给多个域名签发（或一个域名的多个 SAN）时，`_acme-challenge` 下会同时存在
// 多条 TXT。删错一条会让另一次签发在校验阶段莫名失败，而现象是「偶发失败」。
func TestCloudflareRemoveTXTMatchesByValue(t *testing.T) {
	s := newCFServer(t)
	cf := newCF(t, s)
	ctx := context.Background()

	if err := cf.SetTXT(ctx, "_acme-challenge.example.com", "value-A"); err != nil {
		t.Fatal(err)
	}
	if err := cf.SetTXT(ctx, "_acme-challenge.example.com", "value-B"); err != nil {
		t.Fatal(err)
	}
	if s.recordCount() != 2 {
		t.Fatalf("应有两条记录，实际 %d", s.recordCount())
	}

	if err := cf.RemoveTXT(ctx, "_acme-challenge.example.com", "value-A"); err != nil {
		t.Fatal(err)
	}
	if s.recordCount() != 1 {
		t.Fatalf("只该删掉一条，实际剩 %d 条", s.recordCount())
	}
}

// 服务商返回错误时如实报错，且带上它给的原因。
//
// 「凭据没权限」和「域名不在这个账号下」是两件事，处理方式完全不同。
// 吞掉原因只留一句「签发失败」，人只能一个个试。
func TestCloudflareSurfacesAPIError(t *testing.T) {
	s := newCFServer(t)
	s.zoneErr = 9109
	cf := newCF(t, s)

	err := cf.SetTXT(context.Background(), "_acme-challenge.example.com", "v")
	if err == nil {
		t.Fatal("服务商拒绝时必须报错")
	}
	if !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("应带上服务商给的原因，实际 %v", err)
	}
	// 报错里绝不能带上凭据：错误信息会进日志、进告警、进工单
	if strings.Contains(err.Error(), "cf-token-secret") {
		t.Errorf("报错里泄漏了凭据：%v", err)
	}
}

// 找不到 zone 时说清楚是哪个域名，而不是一句「失败」。
func TestCloudflareUnknownZone(t *testing.T) {
	s := newCFServer(t)
	cf := newCF(t, s)

	err := cf.SetTXT(context.Background(), "_acme-challenge.other-domain.net", "v")
	if err == nil {
		t.Fatal("域名不在账号下时应报错")
	}
	if !strings.Contains(err.Error(), "other-domain.net") {
		t.Errorf("应指出是哪个域名，实际 %v", err)
	}
}

// Cloudflare 不支持按线路的加权解析（那是付费的 Load Balancing）。
//
// 如实说「不支持」，而不是悄悄按等权重写下去——后者会让人以为权重配好了，
// 而实际流量是平均分的。
func TestCloudflareDeclaresNoLineSupport(t *testing.T) {
	cf := newCF(t, newCFServer(t))
	if cf.SupportsLines() {
		t.Error("Cloudflare 没有 ISP 线路概念，不该声称支持")
	}
	if cf.SupportsWeights() {
		t.Error("加权解析是 Cloudflare 的付费 Load Balancing，免费套餐不支持")
	}
}
