package dnsprovider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xltxb/edge_caddy/internal/dnsprovider"
)

// dpServer 按 DNSPod（dnsapi.cn）真实形状应答。
//
// DNSPod 的怪癖必须照原样模拟，否则测的就不是真的对接：
//   - 全部走 POST + form 编码，不是 JSON
//   - 凭据是 login_token=<ID>,<Token> 这一个字段
//   - HTTP 恒为 200，成败看 status.code（"1" 是成功）
//   - 记录里的 weight 在未设置时是 JSON null，不是 0
type dpServer struct {
	srv *httptest.Server

	mu      sync.Mutex
	calls   []dpCall
	records []map[string]any
	nextID  int
	// failWith 非空时，所有接口返回这个 status.code
	failWith string
	failMsg  string
}

type dpCall struct {
	path string
	form url.Values
}

func newDPServer(t *testing.T) *dpServer {
	t.Helper()
	s := &dpServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		s.mu.Lock()
		s.calls = append(s.calls, dpCall{path: r.URL.Path, form: r.PostForm})
		fail, msg := s.failWith, s.failMsg
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		// DNSPod 无论成败都回 200，错误在 status.code 里
		if fail != "" {
			_, _ = w.Write([]byte(`{"status":{"code":"` + fail + `","message":"` + msg + `"}}`))
			return
		}

		switch r.URL.Path {
		case "/Record.List":
			s.mu.Lock()
			recs := append([]map[string]any{}, s.records...)
			s.mu.Unlock()
			if len(recs) == 0 {
				// 没有记录时 DNSPod 返回的是错误码 10，不是空列表
				_, _ = w.Write([]byte(`{"status":{"code":"10","message":"No records"}}`))
				return
			}
			out, _ := json.Marshal(map[string]any{
				"status":  map[string]string{"code": "1", "message": "Action completed successful"},
				"domain":  map[string]any{"id": 2317346, "name": "example.com"},
				"records": recs,
			})
			_, _ = w.Write(out)

		case "/Record.Create":
			s.mu.Lock()
			s.nextID++
			rec := map[string]any{
				"id": jsonNum(s.nextID), "name": r.PostForm.Get("sub_domain"),
				"type": r.PostForm.Get("record_type"), "value": r.PostForm.Get("value"),
				"line":    lineOfID(r.PostForm.Get("record_line_id")),
				"line_id": r.PostForm.Get("record_line_id"),
				"ttl":     r.PostForm.Get("ttl"), "weight": weightOrNull(r.PostForm.Get("weight")),
			}
			s.records = append(s.records, rec)
			s.mu.Unlock()
			out, _ := json.Marshal(map[string]any{
				"status": map[string]string{"code": "1", "message": "Action completed successful"},
				"record": rec,
			})
			_, _ = w.Write(out)

		case "/Record.Modify":
			id := r.PostForm.Get("record_id")
			s.mu.Lock()
			for i, rec := range s.records {
				if jsonNumStr(rec["id"]) == id {
					s.records[i]["value"] = r.PostForm.Get("value")
					s.records[i]["weight"] = weightOrNull(r.PostForm.Get("weight"))
					s.records[i]["line_id"] = r.PostForm.Get("record_line_id")
					s.records[i]["line"] = lineOfID(r.PostForm.Get("record_line_id"))
				}
			}
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"Action completed successful"}}`))

		case "/Record.Remove":
			id := r.PostForm.Get("record_id")
			s.mu.Lock()
			kept := s.records[:0]
			for _, rec := range s.records {
				if jsonNumStr(rec["id"]) != id {
					kept = append(kept, rec)
				}
			}
			s.records = kept
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"Action completed successful"}}`))

		default:
			_, _ = w.Write([]byte(`{"status":{"code":"7","message":"Action not found"}}`))
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func jsonNum(i int) json.Number { return json.Number(strconv.Itoa(i)) }
func jsonNumStr(v any) string   { return strings.Trim(strings.TrimSpace(toStr(v)), `"`) }
func toStr(v any) string        { b, _ := json.Marshal(v); return strings.Trim(string(b), `"`) }
func weightOrNull(s string) any {
	if s == "" {
		return nil // DNSPod 未设权重时返回 JSON null，不是 0
	}
	return json.Number(s)
}
func lineOfID(id string) string {
	switch id {
	case "0":
		return "默认"
	case "10=0":
		return "电信"
	case "10=1":
		return "联通"
	case "10=3":
		return "移动"
	case "80=0":
		return "境外"
	}
	return "默认"
}

func (s *dpServer) callsTo(path string) []dpCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []dpCall
	for _, c := range s.calls {
		if c.path == path {
			out = append(out, c)
		}
	}
	return out
}

func (s *dpServer) seed(recs ...map[string]any) {
	s.mu.Lock()
	s.records = append(s.records, recs...)
	s.nextID += len(recs)
	s.mu.Unlock()
}

func newDP(t *testing.T, s *dpServer) *dnsprovider.DNSPod {
	t.Helper()
	return dnsprovider.NewDNSPod(dnsprovider.DNSPodConfig{
		ID: "12345", Token: "dp-token-secret", BaseURL: s.srv.URL,
	})
}

// 凭据是 login_token=<ID>,<Token>，且必须走 POST 表单不进 URL。
func TestDNSPodCredentialFormat(t *testing.T) {
	s := newDPServer(t)
	dp := newDP(t, s)

	_ = dp.SetTXT(context.Background(), "_acme-challenge.example.com", "v1")

	calls := s.callsTo("/Record.Create")
	if len(calls) != 1 {
		t.Fatalf("应调用一次 Record.Create，实际 %d 次", len(calls))
	}
	if got := calls[0].form.Get("login_token"); got != "12345,dp-token-secret" {
		t.Errorf("凭据格式应为 ID,Token，实际 %q", got)
	}
	if got := calls[0].form.Get("format"); got != "json" {
		t.Errorf("应显式要求 JSON 响应，实际 %q", got)
	}
}

// TXT 记录的子域名要从完整域名里剥出来：DNSPod 收的是 sub_domain + domain，
// 不是完整 FQDN。传错会在根域下建出一条 `_acme-challenge.example.com.example.com`。
func TestDNSPodSplitsSubDomain(t *testing.T) {
	s := newDPServer(t)
	dp := newDP(t, s)

	if err := dp.SetTXT(context.Background(), "_acme-challenge.example.com", "v1"); err != nil {
		t.Fatal(err)
	}
	f := s.callsTo("/Record.Create")[0].form
	if got := f.Get("domain"); got != "example.com" {
		t.Errorf("主域应为 example.com，实际 %q", got)
	}
	if got := f.Get("sub_domain"); got != "_acme-challenge" {
		t.Errorf("子域应为 _acme-challenge，实际 %q", got)
	}
	if got := f.Get("record_type"); got != "TXT" {
		t.Errorf("记录类型应为 TXT，实际 %q", got)
	}
}

// status.code 非 "1" 一律是失败，哪怕 HTTP 是 200。
//
// DNSPod 无论成败都回 200。只看 HTTP 状态码的话，凭据过期会被当成成功——
// 而现象是「保存成功但解析没变」。
func TestDNSPodNon200StatusCodeIsFailure(t *testing.T) {
	s := newDPServer(t)
	s.failWith = "-1"
	s.failMsg = "登录失败"
	dp := newDP(t, s)

	err := dp.SetTXT(context.Background(), "_acme-challenge.example.com", "v")
	if err == nil {
		t.Fatal("status.code 非 1 时必须报错，哪怕 HTTP 是 200")
	}
	if !strings.Contains(err.Error(), "登录失败") {
		t.Errorf("应带上服务商给的原因，实际 %v", err)
	}
	if strings.Contains(err.Error(), "dp-token-secret") {
		t.Errorf("报错里泄漏了凭据：%v", err)
	}
}

// 「没有记录」不是错误。
//
// DNSPod 对空列表返回 code 10。把它当成错误的话，一个还没配过解析的域名
// 会让整个调度页报错，而正确的表现是「线上什么都没有」。
func TestDNSPodEmptyRecordListIsNotAnError(t *testing.T) {
	s := newDPServer(t)
	dp := newDP(t, s)

	recs, err := dp.ListA(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("空列表不该报错: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("应返回空列表，实际 %d 条", len(recs))
	}
}

// 读回记录时把线路与权重解出来，未设权重的读成 0 而不是崩掉。
func TestDNSPodListAParsesLineAndWeight(t *testing.T) {
	s := newDPServer(t)
	s.seed(
		map[string]any{"id": "111", "name": "@", "type": "A", "value": "1.1.1.1",
			"line": "电信", "line_id": "10=0", "ttl": "600", "weight": json.Number("30")},
		map[string]any{"id": "112", "name": "@", "type": "A", "value": "2.2.2.2",
			"line": "默认", "line_id": "0", "ttl": "600", "weight": nil},
		// 非 A 记录要被过滤掉，否则调度会把 MX 也当成节点
		map[string]any{"id": "113", "name": "@", "type": "MX", "value": "mail.example.com",
			"line": "默认", "line_id": "0", "ttl": "600", "weight": nil},
	)
	dp := newDP(t, s)

	recs, err := dp.ListA(context.Background(), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("应只返回 2 条 A 记录（MX 要过滤掉），实际 %d 条", len(recs))
	}
	byIP := map[string]dnsprovider.ARecord{}
	for _, r := range recs {
		byIP[r.Value] = r
	}
	if got := byIP["1.1.1.1"]; got.Line != dnsprovider.LineTelecom || got.Weight != 30 {
		t.Errorf("电信 30 权重解析错误：%+v", got)
	}
	// weight 为 JSON null 时读成 0，而不是让整次解析失败
	if got := byIP["2.2.2.2"]; got.Weight != 0 {
		t.Errorf("未设权重应读成 0，实际 %d", got.Weight)
	}
}

// 落地调度计划：已有的改、缺的建、多出来的删。
//
// 不是「全删再全建」：那会在中间留下一个解析为空的窗口，域名在那几秒里
// 谁都访问不了。
func TestDNSPodApplyPlanReconciles(t *testing.T) {
	s := newDPServer(t)
	s.seed(
		// 已有：电信 1.1.1.1 权重 10（要改成 50）
		map[string]any{"id": "111", "name": "@", "type": "A", "value": "1.1.1.1",
			"line": "电信", "line_id": "10=0", "ttl": "600", "weight": json.Number("10")},
		// 已有：电信 9.9.9.9（计划里没有，要删）
		map[string]any{"id": "112", "name": "@", "type": "A", "value": "9.9.9.9",
			"line": "电信", "line_id": "10=0", "ttl": "600", "weight": json.Number("10")},
	)
	dp := newDP(t, s)

	err := dp.ApplyPlan(context.Background(), "example.com", []dnsprovider.Target{
		{NodeID: "node-a", IP: "1.1.1.1", Line: dnsprovider.LineTelecom, Weight: 50},
		{NodeID: "node-b", IP: "2.2.2.2", Line: dnsprovider.LineTelecom, Weight: 50},
	})
	if err != nil {
		t.Fatal(err)
	}

	if n := len(s.callsTo("/Record.Modify")); n != 1 {
		t.Errorf("已有记录应改而不是删了重建，Modify 次数 %d", n)
	}
	if n := len(s.callsTo("/Record.Create")); n != 1 {
		t.Errorf("缺的那条应新建，Create 次数 %d", n)
	}
	if n := len(s.callsTo("/Record.Remove")); n != 1 {
		t.Errorf("计划外的那条应删除，Remove 次数 %d", n)
	}

	recs, err := dp.ListA(context.Background(), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("落地后应剩 2 条，实际 %d 条", len(recs))
	}
	for _, r := range recs {
		if r.Weight != 50 {
			t.Errorf("%s 权重应为 50，实际 %d", r.Value, r.Weight)
		}
	}
}

// **绝不允许把解析清空**。
//
// 空计划意味着所有节点都被摘了。真落地下去，域名会解析成空——比继续解析到
// 一台可能只是心跳抖动的机器糟得多。这道线在客户端这一层也守一遍，
// 因为调度那边的归一化逻辑一旦出错，后果全落在这里。
func TestDNSPodRefusesEmptyPlan(t *testing.T) {
	s := newDPServer(t)
	s.seed(map[string]any{"id": "111", "name": "@", "type": "A", "value": "1.1.1.1",
		"line": "默认", "line_id": "0", "ttl": "600", "weight": json.Number("10")})
	dp := newDP(t, s)

	if err := dp.ApplyPlan(context.Background(), "example.com", nil); err == nil {
		t.Fatal("空计划会把域名解析清空，必须拒绝")
	}
	if n := len(s.callsTo("/Record.Remove")); n != 0 {
		t.Errorf("拒绝之后不该动任何记录，实际删了 %d 条", n)
	}
}

func TestDNSPodDeclaresLineAndWeightSupport(t *testing.T) {
	dp := newDP(t, newDPServer(t))
	if !dp.SupportsLines() || !dp.SupportsWeights() {
		t.Error("DNSPod 原生支持线路与权重，应如实声明")
	}
}
