package dnsctl_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/store"
)

// 结构体字段 → 设置里的 key。**不认识的字段会让测试红**，
// 而那正是这条测试的用处：新加一个被拼进 URL 的字段时，
// 它逼你在这里登记，顺带逼你回答「它必填吗」。
var urlFieldToSettingKey = map[string]string{
	"AccountID": "account_id",
	"ZoneID":    "zone_id",
	// Hostname 不是设置项，它由 domain + sub 拼出来，
	// 而那两项在通用必填里已经守住了。
	"Hostname": "",
	"Base":     "", // API 根地址，来自代码常量 / 测试注入，不是用户填的

	// ID 是 Cloudflare 回给我们的 pool / lb 标识，不是用户填的，
	// 所以「必填」这条对它没有意义。
	//
	// **而它空了照样会拼出 //** —— 只不过原因在对方那边（返回了一条
	// 没有 id 的记录），不是设置页少填了什么。守它的是 call() 里那道
	// 就地拦截：那道不问值从哪来，只问拼出来的路径完不完整。
	"ID": "",
}

// TestEveryConfigValueInAURLPathIsRequired 钉的是一条**通用规则**：
//
//	**一个会被拼进请求路径的配置值，必须是必填的。**
//
// 少了它不会得到「缺参数」，会拼出一个少一段的 URL，然后由对方回一句
// 用它自己的词汇写的错误。灰度上撞到的原话：
//
//	GET /accounts//load_balancers/pools
//	7003 Could not route to /client/v4/accounts/load_balancers/pools,
//	     perhaps your object identifier is invalid?
//
// **那句话里没有一个字提到我们的字段名。** 它把人送去查 token 和权限，
// 而真正的原因是设置页上一个框没填——更糟的是，那个框当时在界面上
// 根本不渲染，人再仔细也填不了它。
//
// 这条是静态的：它不需要真去调 Cloudflare，也就不会因为没有凭据而被跳过。
// **一条要凭据才能跑的检查，等于一条不跑的检查**——今天已经数到第二次了。
func TestEveryConfigValueInAURLPathIsRequired(t *testing.T) {
	dir := "."
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读不到 %s：%v", dir, err)
	}

	// kind 由文件名定：这个包里一个服务商一个文件。
	kinds := map[string]string{"cloudflare.go": "cloudflare", "dnspod.go": "dnspod"}
	seenFiles := 0

	for _, e := range ents {
		kind, ok := kinds[e.Name()]
		if !ok {
			continue
		}
		seenFiles++

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatalf("解析 %s：%v", e.Name(), err)
		}

		required := map[string]bool{}
		// 拿一份「什么都没填」的配置问必填项 —— **判据只有一份**，
		// 就是 store.MissingFields 本身，不是它的抄本。
		for _, mode := range []string{"api_token", "global_key"} {
			cfg := store.DNSProviderSettings{Kind: kind, CredentialMode: mode}
			for _, k := range cfg.MissingFields() {
				required[k] = true
			}
		}

		ast.Inspect(f, func(n ast.Node) bool {
			be, ok := n.(*ast.BinaryExpr)
			if !ok || be.Op != token.ADD || !hasPathLiteral(be) {
				return true
			}
			for _, field := range selectorFields(be) {
				key, known := urlFieldToSettingKey[field]
				if !known {
					t.Errorf("%s：字段 %s 被拼进了请求路径，而 urlFieldToSettingKey "+
						"不认识它。登记它，并回答一个问题：它空了会拼出什么？",
						e.Name(), field)
					continue
				}
				if key == "" {
					continue // 明确不是用户填的设置项
				}
				if !required[key] {
					t.Errorf("%s：%s 被拼进请求路径，而 MissingFields 不要求填它。"+
						"空了就会拼出 // —— 对方回的会是「路由不到」这类和缺字段"+
						"毫无关系的话，而它不认识我们的字段名", e.Name(), key)
				}
			}
			return true
		})
	}

	// 装置自检：一个文件都没扫到时上面的循环不报任何错，而那是「没跑」不是「没问题」。
	if seenFiles != len(kinds) {
		t.Fatalf("只扫到 %d 个服务商文件（期望 %d）—— 文件改名了？"+
			"这条测试此刻什么也没检查", seenFiles, len(kinds))
	}
}

// hasPathLiteral 说这串加法里有没有一个看起来像路径的字面量。
func hasPathLiteral(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if lit, ok := x.(*ast.BasicLit); ok && lit.Kind == token.STRING &&
			strings.HasPrefix(strings.Trim(lit.Value, `"`), "/") {
			found = true
		}
		return !found
	})
	return found
}

// selectorFields 收这串表达式里所有 `x.Field` 的 Field 名。
func selectorFields(n ast.Node) []string {
	var out []string
	ast.Inspect(n, func(x ast.Node) bool {
		if se, ok := x.(*ast.SelectorExpr); ok {
			if _, isIdent := se.X.(*ast.Ident); isIdent {
				out = append(out, se.Sel.Name)
			}
		}
		return true
	})
	return out
}
