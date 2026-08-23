package store_test

import (
	"testing"

	"github.com/xltxb/edge_caddy/internal/store"
)

// TestCloudflareNeedsMoreThanTheCommonThree 钉的是**判据要知道是哪一家**。
//
// 灰度上真实发生的：Cloudflare 只填 kind / domain / token 就保存成功了，
// 而推权重时 Cloudflare 回 `GET /accounts//load_balancers/pools`、错误码 7003
// 「Could not route to ...」——一句会把人送去查 token 权限的话，
// 而真正的原因是 account_id 一个字都没填。
//
// **通用三项齐全 ≠ 这一家能用。** 判据缺了服务商这一维时，
// 它对 Cloudflare 的回答一直是「够了」，而那是一句谎话，
// 且它要等到真去推解析的那一刻、由对方用另一套词汇说破。
func TestCloudflareNeedsMoreThanTheCommonThree(t *testing.T) {
	base := store.DNSProviderSettings{
		Kind: "cloudflare", Domain: "example.com", Credential: "tok",
	}

	// 通用三项齐全，而这一家用不了。
	miss := base.MissingFields()
	for _, want := range []string{"account_id", "zone_id"} {
		if !contains(miss, want) {
			t.Errorf("少了 %s 却说配置完整（missing=%v）—— "+
				"它会被存下、界面显示已配置，然后在推解析时"+
				"由 Cloudflare 回一句「路由不到」", want, miss)
		}
	}
	if base.Usable() {
		t.Error("Usable() 说这份配置能用，而它拼出来的是 /accounts//load_balancers/pools")
	}

	// 补齐之后要放行——一条只会拒绝的校验和拒绝一切没有区别。
	full := base
	full.AccountID, full.ZoneID = "acc", "zone"
	if !full.Usable() {
		t.Errorf("补齐 account_id / zone_id 之后仍被拒：%v", full.MissingFields())
	}

	// global_key 模式额外要 email；api_token 模式不要。
	gk := full
	gk.CredentialMode = "global_key"
	if !contains(gk.MissingFields(), "email") {
		t.Error("global_key 模式缺 email 却说完整 —— X-Auth-Email 配不上就是 401")
	}
	if contains(full.MissingFields(), "email") {
		t.Error("api_token 模式不需要 email，不该拦")
	}
}

// TestDNSPodIsNotDraggedIntoCloudflaresRequirements 守的是反向：
// 给 Cloudflare 加必填项时，别顺手把另一家也一起拦了。
func TestDNSPodIsNotDraggedIntoCloudflaresRequirements(t *testing.T) {
	p := store.DNSProviderSettings{
		Kind: "dnspod", Domain: "example.com", Credential: "id,token",
	}
	if !p.Usable() {
		t.Errorf("DNSPod 被 Cloudflare 的必填项拦下了：%v", p.MissingFields())
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

// TestRequirementsAreDerivedNotCopied 钉的是**清单与判据同源**。
//
// 前端拿 dns_provider_requirements 检查「这个 kind+mode 下该有的输入框
// 都渲染出来了吗」。**一份会和判据分叉的清单，守出来的一致性是假的**——
// 它会在两边都错的时候说「一致」。
//
// 判法是逆着来：拿清单里报的字段逐个填进去，填完必须 Usable。
// 少报一个的话，填完仍然不 Usable，这条就红。
func TestRequirementsAreDerivedNotCopied(t *testing.T) {
	req := store.ProviderRequirements()

	if len(req) != len(store.ProviderKinds) {
		t.Fatalf("清单里有 %d 家，而 ProviderKinds 有 %d 家",
			len(req), len(store.ProviderKinds))
	}

	filled := 0
	for kind, byMode := range req {
		for mode, fields := range byMode {
			cfg := store.DNSProviderSettings{Kind: kind, CredentialMode: mode}
			for _, f := range fields {
				switch f {
				case "domain":
					cfg.Domain = "example.com"
				case "credential":
					cfg.Credential = "x"
				case "account_id":
					cfg.AccountID = "acc"
				case "zone_id":
					cfg.ZoneID = "zone"
				case "email":
					cfg.Email = "a@b.c"
				default:
					// **不认识的字段名要红。** 加了新必填项而这里没跟上时，
					// 下面那个 Usable 断言会因为没填它而失败，
					// 而失败信息会指向「没填」而不是「不认识」——
					// 分开报，别让一个错误装成另一个。
					t.Errorf("%s/%s：清单里报了 %q，而这条测试不认识它。"+
						"填进去的办法要在这里补上", kind, mode, f)
				}
				filled++
			}
			if !cfg.Usable() {
				t.Errorf("%s/%s：按清单填全之后仍然不可用，还缺 %v —— "+
					"清单少报了东西，而界面会照它渲染，于是那个框不会出现",
					kind, mode, cfg.MissingFields())
			}
		}
	}

	// 装置自检：一个字段都没填过的话上面全是空转。
	if filled < 8 {
		t.Fatalf("只填了 %d 个字段 —— 清单是空的，这条测试此刻什么也没证明", filled)
	}

	// 反向：kind 不在清单里（它是选择器，不是要填的字段）。
	for kind, byMode := range req {
		for mode, fields := range byMode {
			for _, f := range fields {
				if f == "kind" {
					t.Errorf("%s/%s 的清单里有 kind —— 它是选择器本身，"+
						"界面上找不到一个叫 kind 的输入框", kind, mode)
				}
			}
		}
	}
}
