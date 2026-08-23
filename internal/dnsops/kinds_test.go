package dnsops_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"

	"github.com/xltxb/edge_caddy/internal/store"
)

// TestAssemblyHandlesEveryKnownKind 钉的是**第三份名单不许存在**。
//
// 认得哪些服务商这件事此前写在三个地方：settings 的校验（两个写死的字符串）、
// dnsops 的装配 switch、以及各家自己的 Caps。加一家时改了一处忘了另一处，
// 症状分两种，而两种都不报错：
//
//	校验放行、装配不认  →  保存成功，推解析时「未知的 DNS 服务商」
//	装配认、校验不放行  →  代码支持它，而人存不进去
//
// 校验那一侧已经改成读 store.ProviderKinds。这条守的是装配那一侧——
// 它必须是 switch（每家 new 的类型不同），所以用 AST 比对 case，
// 而不是把它也改成查表。
func TestAssemblyHandlesEveryKnownKind(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "orchestrator.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 orchestrator.go：%v", err)
	}

	var cases []string
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			return true
		}
		sel, ok := sw.Tag.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Kind" {
			return true
		}
		found = true
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, e := range cc.List {
				if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					cases = append(cases, lit.Value[1:len(lit.Value)-1])
				}
			}
		}
		return true
	})

	// 装置自检：找不到那个 switch 时，下面的比对是拿空集比空集。
	if !found {
		t.Fatal("orchestrator.go 里找不到 `switch cfg.Kind` —— " +
			"改写法了？这条测试此刻什么也没检查")
	}

	sort.Strings(cases)
	known := append([]string(nil), store.ProviderKinds...)
	sort.Strings(known)

	for _, k := range known {
		if !contains(cases, k) {
			t.Errorf("store.ProviderKinds 里有 %q，而装配的 switch 不认它 —— "+
				"人能把它存进去，推解析时才报「未知的 DNS 服务商」", k)
		}
	}
	for _, c := range cases {
		if !contains(known, c) {
			t.Errorf("装配认得 %q，而 store.ProviderKinds 里没有 —— "+
				"校验会拦下它，于是这段代码永远走不到", c)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
