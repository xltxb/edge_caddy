package api

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xltxb/edge_caddy/internal/model"
)

// 这一组测试盯的是**契约的第三条缝**。
//
// 前端的测试验「前端跟它想象中的后端接得上」，我的端到端测试验「后端按契约行事」——
// 两边都绿，而**契约本身对不对，谁都发现不了**。这一轮已经撞出三次：
// 「空对象会清空」写反了、DNS 服务商字段表缺一半、GET /deploys 的 targets 没写进契约。
// 三次都是靠「拿真前端连真主控」手工撞出来的，没有一次是任何一边的测试报出来的。
//
// web/request-shapes.json 是前端从**类型**里导出的「我会发出去的字段」清单
// （不是手写的：shape<T>() 强制清单与 T 的键完全一致，漏一个多一个都编译不过）。
// 这里做的事只有一件：**前端会发的字段，后端必须真的收得下**。
//
// 方向是刻意单向的。后端收得下而前端不发的字段（比如 master_endpoint）不是错，
// 那是还没做的界面；反过来才是错——前端发了、后端的结构体里没有，
// 于是 encoding/json 静默丢掉，返回 code 0，界面显示「已保存」。

type bodyKind int

const (
	bodyStruct bodyKind = iota // 绑定到一个 Go 结构体
	bodyNone                   // 不带请求体
	bodyAny                    // 任意 JSON，后端不看字段名
)

type bodySpec struct {
	kind  bodyKind
	proto any    // kind == bodyStruct 时，handler 真正绑定的那个类型的零值
	why   string // kind != bodyStruct 时，为什么没有字段集
}

// requestBodies 把每个**带请求体的写端点**钉到 handler 真正绑定的那个类型上。
//
// **这张表是手写的，但它写不出过期的字段名**——字段集是 reflect 从真类型上取的，
// 表里只有类型本身。类型改了字段，这里跟着变；handler 换了绑定的类型而表没改，
// 那是唯一一种能骗过它的写法，而它同时会被下面那条「每个写路由都要有一条」挡住。
var requestBodies = map[string]bodySpec{
	"POST /auth/login":  {proto: loginReq{}},
	"POST /auth/logout": {kind: bodyNone, why: "登出只认 Cookie"},

	"POST /nodes/token":     {proto: tokenReq{}},
	"POST /nodes/:p/push":   {kind: bodyNone, why: "重推没有参数"},
	"POST /nodes/:p/dns":    {proto: dnsToggleReq{}},
	"POST /nodes/:p/probe":  {kind: bodyNone, why: "拨测没有参数"},
	"POST /nodes/:p/drain":  {proto: drainReq{}},
	"POST /nodes/:p/rejoin": {kind: bodyNone, why: "重新上线没有参数"},
	"PUT /nodes/:p":         {proto: nodeMetaReq{}},
	"DELETE /nodes/:p":      {kind: bodyNone, why: "删除只认路径；前提（必须先下线）由后端查，不靠请求体确认"},

	"POST /routes":      {proto: model.Route{}},
	"PUT /routes/:p":    {proto: model.Route{}},
	"DELETE /routes/:p": {kind: bodyNone, why: "删除只认路径"},

	"PUT /rules/:p":    {proto: ruleReq{}},
	"DELETE /rules/:p": {kind: bodyNone, why: "删除只认路径"},

	"PUT /policies/:p": {proto: model.Policy{}},

	// 草稿存的是原始 JSON：handler 只校验 json.Valid，**不看字段名**。
	// 前端那边同样给不出固定字段集（键随资源种类而变），两边都没有集合，
	// 那么这里就没有可比的东西——**写成空集会是假话**，它会读成「前端什么都不发」。
	"PUT /drafts/:p": {kind: bodyAny, why: "草稿是资源 spec 的 Partial，键随资源种类而变；后端原样存原始 JSON"},
	"DELETE /drafts": {kind: bodyNone, why: "放弃全部草稿，没有参数"},

	"POST /deploys":             {proto: deployReq{}},
	"POST /deploys/preview":     {proto: deployReq{}},
	"POST /deploys/:p/rollback": {kind: bodyNone, why: "回滚目标在路径里"},

	"PUT /dns/weights": {proto: weightsReq{}},

	"PUT /certs/:p": {proto: importCertReq{}},

	"PUT /settings":     {proto: systemReq{}},
	"PUT /alerts":       {proto: alertsReq{}},
	"POST /alerts/test": {proto: testAlertReq{}},
}

// normalize 把 gin 的路由写法与前端的写法碾成同一种。
//
// 两边的路径变量名不一样（后端 :id，前端 :cfg），而变量**叫什么**从来不是契约的一部分。
func normalize(method, path string) string {
	p := strings.TrimPrefix(path, "/api/v1")
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") || strings.HasPrefix(s, "*") {
			segs[i] = ":p"
		}
	}
	return method + " " + strings.Join(segs, "/")
}

func writeRoutes(t *testing.T) map[string]bool {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := New(Options{})
	out := map[string]bool{}
	for _, ri := range r.Routes() {
		switch ri.Method {
		case "POST", "PUT", "DELETE", "PATCH":
			out[normalize(ri.Method, ri.Path)] = true
		}
	}
	// **自检。** 下面两条都是「集合相等」，而 r.Routes() 返回空时它们会
	// 退化成「空集等于空集」——一条也不比，全绿。
	if len(out) < 20 {
		t.Fatalf("装置坏了：只从路由表里拿到 %d 个写端点，本该有二十几个", len(out))
	}
	return out
}

// 每个写路由都得在 requestBodies 里有一条。
//
// **「多写了一条」会被下面那条断言报出来，「少写一条」谁都不会提**——
// 新加一个端点、忘了登记，那个端点就悄悄退出了这套检查，
// 而它退出的样子和「它本来就不需要检查」一模一样。
func TestEveryWriteRouteHasARequestBodySpec(t *testing.T) {
	have := writeRoutes(t)

	var missing []string
	for ep := range have {
		if _, ok := requestBodies[ep]; !ok {
			missing = append(missing, ep)
		}
	}
	var stale []string
	for ep := range requestBodies {
		if !have[ep] {
			stale = append(stale, ep)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("这些写端点还没登记请求体类型（新加端点时补上）：\n  %s",
			strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("requestBodies 里有路由表里不存在的端点（端点删了或改了路径）：\n  %s",
			strings.Join(stale, "\n  "))
	}
}

// jsonFields 取一个结构体在 encoding/json 眼里的顶层字段名。
func jsonFields(t reflect.Type) map[string]reflect.Type {
	t = deref(t)
	out := map[string]reflect.Type{}
	if t.Kind() != reflect.Struct {
		return out
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		// 内嵌结构体的字段在 JSON 里是平铺的（ruleReq 内嵌 model.Rule 就是这样）。
		if f.Anonymous && name == "" {
			for k, v := range jsonFields(f.Type) {
				out[k] = v
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	return t
}

// resolve 顺着 "dns_provider" / "lines[].entries[]" 这样的路径走到目标结构体。
//
// 最后一段落在一个叶子字段上时（前端用 "dns_provider.clear" 表示 dns_provider
// 的另一种用法），目标就是它的父结构体。
func resolve(root reflect.Type, path string) (map[string]reflect.Type, string) {
	cur := root
	segs := strings.Split(path, ".")
	for i, seg := range segs {
		name := strings.TrimSuffix(seg, "[]")
		fields := jsonFields(cur)
		ft, ok := fields[name]
		if !ok {
			return nil, "字段 " + name + " 在 " + cur.String() + " 里不存在"
		}
		next := deref(ft)
		if next.Kind() == reflect.Struct {
			cur = next
			continue
		}
		if i == len(segs)-1 {
			return fields, "" // 叶子字段：比的是它所在的那一层
		}
		return nil, "字段 " + name + " 不是对象，走不下去"
	}
	return jsonFields(cur), ""
}

type shapeFile struct {
	Endpoints map[string]shapeEntry `json:"endpoints"`
	Nested    map[string]shapeEntry `json:"nested"`
}

type shapeEntry struct {
	Required []string `json:"required"`
	Optional []string `json:"optional"`
	Dynamic  string   `json:"dynamic"`
}

func (e shapeEntry) names() []string {
	out := append([]string{}, e.Required...)
	return append(out, e.Optional...)
}

const shapesPath = "../../web/request-shapes.json"

// 前端会发出去的每一个字段，后端都必须真的收得下。
//
// 收不下的表现不是报错，是**静默丢弃**：encoding/json 忽略不认识的键，
// handler 照常返回 code 0，界面显示「已保存」而那个值从来没存进去。
// 这一族里最坏的一种——报错会让人再试，假象让人走开。
func TestFrontendRequestFieldsAreAccepted(t *testing.T) {
	raw, err := os.ReadFile(shapesPath)
	if err != nil {
		t.Fatalf("读不到前端导出的请求字段表 %s：%v\n"+
			"它由 web 那边的 pnpm gen:requests 生成并提交进仓库。"+
			"文件没了不代表这条检查该跳过——那正好是它该红的时候。", shapesPath, err)
	}
	var sf shapeFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		t.Fatalf("解析 %s 失败：%v", shapesPath, err)
	}
	if len(sf.Endpoints) < 15 {
		t.Fatalf("装置坏了：%s 里只有 %d 个端点，本该有二十几个", shapesPath, len(sf.Endpoints))
	}

	// 端点键先归一化，前端写 :cfg、后端写 :id 的那种差异在这里抹平。
	byEP := map[string]shapeEntry{}
	for k, v := range sf.Endpoints {
		method, path, ok := strings.Cut(k, " ")
		if !ok {
			t.Errorf("%s 里的端点键格式不对：%q", shapesPath, k)
			continue
		}
		byEP[normalize(method, path)] = v
	}

	for ep, entry := range byEP {
		spec, ok := requestBodies[ep]
		if !ok {
			t.Errorf("前端会请求 %s，而后端没有这个端点", ep)
			continue
		}
		switch spec.kind {
		case bodyNone:
			if n := entry.names(); len(n) > 0 {
				t.Errorf("%s 后端不收请求体（%s），而前端会发 %v", ep, spec.why, n)
			}
			continue
		case bodyAny:
			if entry.Dynamic == "" && len(entry.names()) > 0 {
				t.Errorf("%s 后端原样收下整个 JSON（%s），而前端给出了固定字段集 %v —— "+
					"两边对这个端点的理解不一样", ep, spec.why, entry.names())
			}
			continue
		}
		fields := jsonFields(reflect.TypeOf(spec.proto))
		for _, name := range entry.names() {
			if _, ok := fields[name]; !ok {
				t.Errorf("%s：前端会发字段 %q，而后端的 %s 里没有它 —— "+
					"这个值会被静默丢掉，请求照样返回 code 0。后端认得的是 %v",
					ep, name, reflect.TypeOf(spec.proto).Name(), sortedKeys(fields))
			}
		}
	}

	// 嵌套：键形如 "PUT /settings#dns_provider"。
	for k, entry := range sf.Nested {
		head, path, ok := strings.Cut(k, "#")
		if !ok {
			t.Errorf("%s 里的嵌套键格式不对：%q", shapesPath, k)
			continue
		}
		method, p, _ := strings.Cut(head, " ")
		ep := normalize(method, p)
		spec, ok := requestBodies[ep]
		if !ok || spec.kind != bodyStruct {
			t.Errorf("嵌套键 %q 指向的端点 %s 没有结构体请求体", k, ep)
			continue
		}
		fields, why := resolve(reflect.TypeOf(spec.proto), path)
		if why != "" {
			t.Errorf("%s：前端会发嵌套对象 %q，而后端走不到它 —— %s", ep, path, why)
			continue
		}
		for _, name := range entry.names() {
			if _, ok := fields[name]; !ok {
				t.Errorf("%s：前端会在 %q 里发字段 %q，后端没有 —— 会被静默丢掉。后端认得的是 %v",
					ep, path, name, sortedKeys(fields))
			}
		}
	}
}

func sortedKeys(m map[string]reflect.Type) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// StructBodyEndpoints 把上面那张表里**绑定结构体**的端点导给外部测试包。
//
// 导出一个函数而不是导出整张表：外部要的只是「哪些端点该拒未知字段」，
// 类型和原型是这一层的实现细节。
func StructBodyEndpoints() []string {
	out := []string{}
	for ep, spec := range requestBodies {
		if spec.kind == bodyStruct {
			out = append(out, ep)
		}
	}
	sort.Strings(out)
	return out
}
