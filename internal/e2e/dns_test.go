package e2e_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/api"
)

type weightsResp struct {
	Domain string `json:"domain"`
	Lines  []struct {
		Code    string `json:"code"`
		Name    string `json:"name"`
		Entries []struct {
			Node   string  `json:"node"`
			Weight int     `json:"weight"`
			Share  float64 `json:"share"`
		} `json:"entries"`
	} `json:"lines"`
	Capabilities struct {
		Kind    string   `json:"kind"`
		Lines   []string `json:"lines"`
		Weights bool     `json:"weights"`
		Notes   string   `json:"notes"`
	} `json:"capabilities"`
}

func (r *rig) weights() weightsResp {
	r.t.Helper()
	e := r.mustDo("GET", "/dns/weights", nil)
	var w weightsResp
	if err := json.Unmarshal(e.Data, &w); err != nil {
		r.t.Fatal(err)
	}
	return w
}

// 五条线路始终齐全，即使一条都没配 —— 前端按线路分组渲染，
// 缺一条会让那一组凭空消失，而不是显示成「这条线还没配」。
func TestDNSWeightsAlwaysReturnsFiveLines(t *testing.T) {
	r := newRig(t)
	w := r.weights()
	if len(w.Lines) != 5 {
		t.Fatalf("线路数 = %d，想要 5", len(w.Lines))
	}
	for i, code := range []string{"ct", "cu", "cm", "tw", "ov"} {
		if w.Lines[i].Code != code {
			t.Errorf("第 %d 条 = %s，想要 %s", i, w.Lines[i].Code, code)
		}
	}
}

// **没配服务商时如实说清楚**：权重只会保存在本地，不会推到任何地方。
//
// 不说的话，人配了一堆权重、界面显示保存成功，而解析根本没动过。
func TestDNSCapabilitiesSayWhenNoProviderConfigured(t *testing.T) {
	r := newRig(t)
	w := r.weights()
	if w.Capabilities.Kind != "" {
		t.Errorf("还没配服务商，kind 应当为空，实际 %q", w.Capabilities.Kind)
	}
	if !strings.Contains(w.Capabilities.Notes, "尚未配置") {
		t.Errorf("说明里应当讲清还没配服务商: %q", w.Capabilities.Notes)
	}
}

// 没配服务商时权重仍然可以保存 —— 那是本地的意图，
// 没有服务商不代表不能先配好。
func TestDNSWeightsSaveLocallyWithoutProvider(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("PUT", "/dns/weights", map[string]any{
		"lines": []any{map[string]any{
			"code":    "ct",
			"entries": []any{map[string]any{"node": "node-hk-01", "weight": 100}},
		}},
	})

	w := r.weights()
	for _, l := range w.Lines {
		if l.Code != "ct" {
			continue
		}
		if len(l.Entries) != 1 || l.Entries[0].Weight != 100 || l.Entries[0].Share != 100 {
			t.Fatalf("电信线路 = %+v，想要 weight=100 share=100", l.Entries)
		}
		return
	}
	t.Fatal("没找到电信线路")
}

func TestDNSWeightsRejectsUnknownLineAndNegativeWeight(t *testing.T) {
	r := newRig(t)

	_, e := r.do("PUT", "/dns/weights", map[string]any{
		"lines": []any{map[string]any{"code": "火星", "entries": []any{}}},
	})
	if e.Code != api.CodeValidation {
		t.Fatalf("未知线路码 code = %d，想要 %d", e.Code, api.CodeValidation)
	}

	_, e2 := r.do("PUT", "/dns/weights", map[string]any{
		"lines": []any{map[string]any{
			"code":    "ct",
			"entries": []any{map[string]any{"node": "a", "weight": -1}},
		}},
	})
	if e2.Code != api.CodeValidation {
		t.Fatalf("负权重 code = %d，想要 %d", e2.Code, api.CodeValidation)
	}
	var d struct {
		Errors []struct {
			Field string `json:"field"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(e2.Data, &d)
	if len(d.Errors) == 0 || !strings.Contains(d.Errors[0].Field, "weight") {
		t.Fatalf("错误应当定位到具体的权重字段，实际 %+v", d.Errors)
	}
}

// 被摘除的节点占比归零，其余节点重新归一化 —— 端到端验一遍。
func TestDNSSharesRenormalizeWhenNodePaused(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"node-a", "node-b"} {
		token, _ := r.issueTokenFor(id)
		r.startAgent(id, token, t.TempDir())
		r.waitOnline(id)
	}

	r.mustDo("PUT", "/dns/weights", map[string]any{
		"lines": []any{map[string]any{
			"code": "ct",
			"entries": []any{
				map[string]any{"node": "node-a", "weight": 60},
				map[string]any{"node": "node-b", "weight": 40},
			},
		}},
	})

	// 暂停 b 的解析。
	r.mustDo("POST", "/nodes/node-b/dns", map[string]any{"enabled": false})

	w := r.weights()
	for _, l := range w.Lines {
		if l.Code != "ct" {
			continue
		}
		got := map[string]float64{}
		weights := map[string]int{}
		for _, e := range l.Entries {
			got[e.Node] = e.Share
			weights[e.Node] = e.Weight
		}
		if got["node-a"] != 100 || got["node-b"] != 0 {
			t.Fatalf("占比 = %+v，想要 a=100 b=0", got)
		}
		// Weight 是配置值，不该被自愈改掉 —— 人没动过它。
		if weights["node-b"] != 40 {
			t.Errorf("被暂停的节点权重应当保持 40，实际 %d", weights["node-b"])
		}
		return
	}
	t.Fatal("没找到电信线路")
}

// DNS 服务商凭证只写入不回显（PRD §7）。
func TestDNSProviderCredentialNotEchoed(t *testing.T) {
	r := newRig(t)
	const cred = "dnspod-SECRET-TOKEN-42"

	r.mustDo("PUT", "/settings", map[string]any{
		"dns_provider": map[string]any{
			"kind": "dnspod", "domain": "example.com", "sub": "cdn", "credential": cred,
		},
	})

	e := r.mustDo("GET", "/settings", nil)
	if strings.Contains(string(e.Data), "SECRET-TOKEN") {
		t.Fatalf("GET /settings 回显了 DNS 凭证: %s", e.Data)
	}
	var d struct {
		DNSProvider struct {
			Kind       string `json:"kind"`
			Domain     string `json:"domain"`
			Configured bool   `json:"configured"`
		} `json:"dns_provider"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.DNSProvider.Kind != "dnspod" || d.DNSProvider.Domain != "example.com" || !d.DNSProvider.Configured {
		t.Fatalf("设置没保存对: %+v", d.DNSProvider)
	}

	// 不带凭证再保存一次 = 保持不变，不是抹掉。
	r.mustDo("PUT", "/settings", map[string]any{
		"dns_provider": map[string]any{"sub": "www"},
	})
	again := r.mustDo("GET", "/settings", nil)
	var d2 struct {
		DNSProvider struct {
			Sub        string `json:"sub"`
			Configured bool   `json:"configured"`
		} `json:"dns_provider"`
	}
	_ = json.Unmarshal(again.Data, &d2)
	if !d2.DNSProvider.Configured {
		t.Fatal("不带凭证的保存把已配置的凭证抹掉了")
	}
	if d2.DNSProvider.Sub != "www" {
		t.Errorf("其余字段应当被更新，sub = %q", d2.DNSProvider.Sub)
	}
}
