package render_test

import (
	"encoding/json"
	"testing"

	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/render"
)

func route(domain string) model.Route {
	return model.Route{
		Domain: domain, Upstream: "10.8.0.2:8080", Block: model.BlockAbort,
		BodyMax: "5MB", Compress: true, Whitelist: []string{"203.0.113.7"}, Version: 1,
	}
}

// 取出第一条路由里指定 handler 的配置。
func handlerOf(t *testing.T, out []byte, name string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("产物不是合法 JSON: %v", err)
	}
	srv := doc["http"].(map[string]any)["servers"].(map[string]any)["edge"].(map[string]any)
	rt := srv["routes"].([]any)[0].(map[string]any)
	sub := rt["handle"].([]any)[0].(map[string]any)
	if sub["handler"] != "subroute" {
		t.Fatalf("第一个 handler 应为 subroute，实际 %v", sub["handler"])
	}
	// subroute 的最后一条分支是业务 handler 链
	branches := sub["routes"].([]any)
	last := branches[len(branches)-1].(map[string]any)
	for _, h := range last["handle"].([]any) {
		hm := h.(map[string]any)
		if hm["handler"] == name {
			return hm
		}
	}
	t.Fatalf("未找到 handler %q，实际链: %v", name, last["handle"])
	return nil
}

// request_body.max_size 必须是**数字字节数**，不能是 "5MB" 这样的字符串。
//
// 设计稿的 caddyJSON 就是直接把 cfg.bodyMax（字符串）塞进 max_size 的，
// 那份 JSON 原样下发会被 Caddy 整份拒绝。渲染器是下发的唯一权威
// （docs/adr/0007），这个转换必须在这里完成，且必须与人类可读值语义一致：
// MB 是十进制 10^6，MiB 才是 2^20。
func TestBodyMaxRendersAsByteCount(t *testing.T) {
	out, err := render.Caddy([]model.Route{route("api.example.com")})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	h := handlerOf(t, out, "request_body")

	got, ok := h["max_size"].(float64) // JSON 数字反序列化为 float64
	if !ok {
		t.Fatalf("max_size 必须是数字，实际是 %T: %v", h["max_size"], h["max_size"])
	}
	if got != 5_000_000 {
		t.Fatalf("5MB 应为 5000000 字节（十进制 MB），实际 %v", got)
	}
}

// 白名单渲染成 not/remote_ip 拒绝分支，处置方式三种取值必须产出**不同**的响应。
//
// 这条守的是「入库但不生效」：如果渲染器忽略处置方式，三种配置会产出一模一样的
// JSON，用户改了设置却毫无效果，而且没有任何报错能提示他。
func TestWhitelistGuardAndBlockActions(t *testing.T) {
	cases := []struct {
		block  model.BlockAction
		assert func(t *testing.T, deny map[string]any)
	}{
		{model.BlockAbort, func(t *testing.T, d map[string]any) {
			if d["abort"] != true {
				t.Fatalf("abort 应渲染 abort:true，实际 %v", d)
			}
		}},
		{model.Block403, func(t *testing.T, d map[string]any) {
			if d["status_code"] != float64(403) {
				t.Fatalf("403 未生效: %v", d)
			}
		}},
		{model.Block404, func(t *testing.T, d map[string]any) {
			if d["status_code"] != float64(404) {
				t.Fatalf("404 未生效: %v", d)
			}
		}},
	}
	for _, c := range cases {
		r := route("api.example.com")
		r.Block = c.block
		r.Whitelist = []string{"203.0.113.7", "10.8.0.0/24"}
		out, err := render.Caddy([]model.Route{r})
		if err != nil {
			t.Fatalf("渲染失败: %v", err)
		}
		g := firstBranch(t, out)
		m := g["match"].([]any)[0].(map[string]any)
		ranges := m["not"].([]any)[0].(map[string]any)["remote_ip"].(map[string]any)["ranges"].([]any)
		if len(ranges) != 2 || ranges[0] != "203.0.113.7" || ranges[1] != "10.8.0.0/24" {
			t.Fatalf("白名单未正确渲染: %v", ranges)
		}
		c.assert(t, g["handle"].([]any)[0].(map[string]any))
	}
}

// 白名单恰好只有 0.0.0.0/0 时不渲染拒绝分支（设计稿 caddyJSON 的专门判定）。
// 空白名单同理——渲染成「谁都不许进」会把半配置好的路由变成全站故障。
func TestNoGuardWhenAllowAllOrEmpty(t *testing.T) {
	for name, wl := range map[string][]string{
		"仅 0.0.0.0/0": {model.AllowAllCIDR},
		"空":           {},
		"只有空白项":       {"  ", ""},
	} {
		r := route("api.example.com")
		r.Whitelist = wl
		out, err := render.Caddy([]model.Route{r})
		if err != nil {
			t.Fatalf("%s: 渲染失败: %v", name, err)
		}
		if b := firstBranch(t, out); b["match"] != nil {
			t.Errorf("%s: 不应渲染拒绝分支，实际 %v", name, b["match"])
		}
	}
}

func TestCompressOffOmitsEncoder(t *testing.T) {
	r := route("api.example.com")
	r.Compress = false
	out, _ := render.Caddy([]model.Route{r})
	if containsHandler(t, out, "encode") {
		t.Fatal("关闭压缩后仍渲染出 encode handler")
	}
}

// 回源 mTLS 渲染到 reverse_proxy.transport.tls，且用的是**文件路径**而非
// client_certificate_automate——后者会让每台节点自成一个 CA（docs/adr/0009）。
func TestUpstreamMTLSRendersClientCertFiles(t *testing.T) {
	r := route("admin.example.com")
	r.MTLS = true
	out, _ := render.Caddy([]model.Route{r})
	proxy := handlerOf(t, out, "reverse_proxy")
	tls, ok := proxy["transport"].(map[string]any)["tls"].(map[string]any)
	if !ok {
		t.Fatalf("开启 mTLS 后 transport 应含 tls，实际 %v", proxy["transport"])
	}
	if tls["client_certificate_file"] != render.UpstreamCertPath {
		t.Errorf("客户端证书路径不对: %v", tls)
	}
	if _, bad := tls["client_certificate_automate"]; bad {
		t.Error("不得使用 client_certificate_automate——那会让节点本机成为 CA")
	}

	r.MTLS = false
	out2, _ := render.Caddy([]model.Route{r})
	if _, has := handlerOf(t, out2, "reverse_proxy")["transport"].(map[string]any)["tls"]; has {
		t.Error("关闭 mTLS 后不应出现 transport.tls")
	}
}

func TestRejects(t *testing.T) {
	dup := []model.Route{route("api.example.com"), route("api.example.com")}
	bad := route("api.example.com")
	bad.BodyMax = "不是大小"
	huge := route("api.example.com")
	huge.BodyMax = "10EB"
	noUp := route("api.example.com")
	noUp.Upstream = ""
	badBlock := route("api.example.com")
	badBlock.Block = "reject"

	for name, in := range map[string][]model.Route{
		"重复域名":   dup,
		"非法大小":   {bad},
		"超出上限":   {huge},
		"空回源":    {noUp},
		"未知处置方式": {badBlock},
	} {
		if _, err := render.Caddy(in); err == nil {
			t.Errorf("%s 应当渲染失败", name)
		}
	}
}

// 相同输入必须逐字节相同，且与入参顺序无关。
func TestDeterministic(t *testing.T) {
	a := []model.Route{route("b.example.com"), route("a.example.com")}
	b := []model.Route{route("a.example.com"), route("b.example.com")}
	first, err := render.Caddy(a)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		again, _ := render.Caddy(a)
		if string(first) != string(again) {
			t.Fatalf("第 %d 次渲染结果不一致", i)
		}
	}
	other, _ := render.Caddy(b)
	if string(first) != string(other) {
		t.Fatal("入参顺序改变了渲染结果")
	}
}

// ── 测试辅助 ──

func firstBranch(t *testing.T, out []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	srv := doc["http"].(map[string]any)["servers"].(map[string]any)["edge"].(map[string]any)
	rt := srv["routes"].([]any)[0].(map[string]any)
	sub := rt["handle"].([]any)[0].(map[string]any)
	return sub["routes"].([]any)[0].(map[string]any)
}

func containsHandler(t *testing.T, out []byte, name string) bool {
	t.Helper()
	var doc map[string]any
	_ = json.Unmarshal(out, &doc)
	return bytesContains(out, `"`+name+`"`)
}

func bytesContains(b []byte, sub string) bool {
	return len(b) >= len(sub) && indexOf(string(b), sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
