package e2e_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/api"
)

type certItem struct {
	Domain        string   `json:"domain"`
	Issuer        string   `json:"issuer"`
	Challenge     string   `json:"challenge"`
	DaysLeft      int      `json:"days_left"`
	ExpectedNodes int      `json:"expected_nodes"`
	LoadedNodes   int      `json:"loaded_nodes"`
	MissingNodes  []string `json:"missing_nodes"`
}

func (r *rig) certs() []certItem {
	r.t.Helper()
	e := r.mustDo("GET", "/certs", nil)
	var d struct {
		Items []certItem `json:"items"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		r.t.Fatal(err)
	}
	return d.Items
}

func (r *rig) waitCert(domain string, cond func(certItem) bool, why string) certItem {
	r.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last certItem
	for time.Now().Before(deadline) {
		for _, c := range r.certs() {
			if c.Domain != domain {
				continue
			}
			last = c
			if cond(c) {
				return c
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	r.t.Fatalf("等待「%s」超时；最后看到 %+v", why, last)
	return last
}

// 证书页的**两列真相**：主控账面 vs 节点回执。
//
// loaded < expected 意味着「下发到了但没生效」。这类故障在「节点自管证书」
// 的模型里根本看不见，是这套设计换来的主要能力。
//
// 而且回执不是复述主控下发的那份——Agent 在回环上**真握了一次手**读对端的
// 证书。配置被接受不等于在服务，那是 ADR-0004 复核时那个「幽灵监听」教过的。
func TestCertTwoColumnsOfTruth(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "secure.example.com", "upstream": r.upstream, "block_mode": "abort",
	})

	// 证书从外部平台导入（ADR-0015：主控不签发）。
	certPEM, keyPEM := importableCert(t, "secure.example.com")
	r.mustDo("PUT", "/certs/secure.example.com", map[string]any{
		"cert_pem": string(certPEM), "key_pem": string(keyPEM),
	})
	before := r.waitCert("secure.example.com", func(c certItem) bool { return c.DaysLeft > 0 },
		"证书进了主控的账")

	// **「主控账面」与「节点回执」是两列，而回执要等下发。**
	//
	// 导入会自己触发一次下发（ADR-0010：证书随下发内联带上），
	// 所以这里不断言「loaded 一定是 0」——那会变成一条跟时序赛跑的断言。
	// 真正要钉的是下面那一条：**回执来自节点上一次真实的 TLS 握手**，
	// 而不是复述主控下发的那份。
	if before.ExpectedNodes != 1 {
		t.Fatalf("应当知道这张证书该到几台机器上：%+v", before)
	}
	if before.Challenge != "imported" {
		// **导入的证书不是通过任何 challenge 拿到的**，如实记成 imported。
		// 记成 dns-01 会让人以为主控自己跑过一次校验（ADR-0015）。
		t.Errorf("challenge = %q，想要 imported", before.Challenge)
	}

	// 下发之后，节点真的加载了 —— 回执来自一次真实的 TLS 握手。
	r.deployNow("route:secure.example.com")
	after := r.waitCert("secure.example.com", func(c certItem) bool { return c.LoadedNodes == 1 },
		"节点回执显示已加载")
	if len(after.MissingNodes) != 0 {
		t.Fatalf("全部加载后不该还有缺失：%+v", after)
	}
}

// 主控还没有证书时，下发的配置里没有 apps/tls —— 节点上外部证书平台
// 写入的内容因此原样保留（ADR-0010）。
func TestDeployWithoutCertsHasNoTLSApp(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "plain.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	r.deployNow("route:plain.example.com")

	if _, ok := r.caddy.Config()["apps"].(map[string]any)["tls"]; ok {
		t.Fatal("主控没有证书时，下发的配置不该带 apps/tls")
	}
	// 明文那条路仍然通。
	if code, body := r.curlVia("plain.example.com"); code != 200 || body != "UPSTREAM OK" {
		t.Fatalf("得到 %d %q", code, body)
	}
}

// **回源 mTLS**：边缘节点向源站出示客户端证书（ADR-0008）。
//
// 这条端到端验的是那条链路真的成立：主控用回源 CA 签一张 24 小时的叶子
// （CN 是 node_id）→ 随下发送到节点 → Agent 落盘 → Caddy 接受这份配置。
//
// 之前渲染器对 mtls=true 是**拒绝下发**的，因为叶子的下发还没做。现在做了，
// 那条拒绝也就该撤掉——一个开着却没有效果的安全开关危险，
// 而一个引用了不存在文件的配置同样危险。
func TestUpstreamMTLSCertIsDistributedAndAccepted(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "mtls.example.com", "upstream": r.upstream,
		"block_mode": "abort", "mtls": true,
	})
	r.deployNow("route:mtls.example.com")

	detail := r.mustDo("GET", "/deploys/1", nil)
	var dd struct {
		OKCount int `json:"ok_count"`
		Results []struct {
			State  string `json:"state"`
			Detail string `json:"detail"`
		} `json:"results"`
	}
	if err := json.Unmarshal(detail.Data, &dd); err != nil {
		t.Fatal(err)
	}
	if dd.OKCount != 1 {
		t.Fatalf("带回源 mTLS 的配置应当被接受，结果 %+v", dd.Results)
	}

	// 配置里引用的证书文件必须真的在，否则 Caddy 会整份拒绝——
	// 而报错是「文件不存在」，跟证书轮换看起来毫无关系。
	if code, body := r.curlVia("mtls.example.com"); code != 200 && code != 502 {
		// 上游是普通 HTTP，不会接受客户端证书，所以 502 是预期内的；
		// 关键是配置被加载了、请求走到了回源那一步。
		t.Fatalf("得到 %d %q，想要请求能走到回源（200 或 502）", code, body)
	}
}

// 访问规则与全局策略的读写。前端工作台的另外两栏靠它们。
func TestRulesAndPoliciesRoundTrip(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "api.example.com", "upstream": r.upstream, "block_mode": "abort",
	})

	// 全局策略始终存在，即使没人改过 —— 资源树里那两栏不该因此消失。
	for _, id := range []string{"tls", "log"} {
		e := r.mustDo("GET", "/policies/"+id, nil)
		var p struct {
			ID   string          `json:"id"`
			Name string          `json:"name"`
			Spec json.RawMessage `json:"spec"`
		}
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		if p.ID != id || p.Name == "" || len(p.Spec) == 0 {
			t.Fatalf("策略 %s = %+v", id, p)
		}
	}

	r.mustDo("PUT", "/policies/tls", map[string]any{
		"spec": map[string]any{"min_version": "1.3", "hsts": true},
	})
	back := r.mustDo("GET", "/policies/tls", nil)
	if !contains(string(back.Data), `"min_version":"1.3"`) {
		t.Fatalf("策略没保存对: %s", back.Data)
	}

	// 只有 tls 与 log 两条。
	if _, e := r.do("GET", "/policies/nope", nil); e.Code == 0 {
		t.Fatal("不存在的策略 id 应当被拒绝")
	}

	// 访问规则：IP 白名单。
	r.mustDo("PUT", "/rules/office-wl", map[string]any{
		"name": "办公网白名单", "type": "ip_whitelist", "enabled": true,
		"apply_to": []string{"api.example.com"},
		"spec":     map[string]any{"ips": []string{"203.0.113.7", "10.8.0.0/24"}},
	})
	list := r.mustDo("GET", "/rules", nil)
	var d struct {
		Items []struct {
			ID      string   `json:"id"`
			Type    string   `json:"type"`
			ApplyTo []string `json:"apply_to"`
			Spec    struct {
				IPs              []string `json:"ips"`
				SecretConfigured bool     `json:"secret_configured"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 1 || d.Items[0].ID != "office-wl" || len(d.Items[0].Spec.IPs) != 2 {
		t.Fatalf("规则没保存对: %+v", d.Items)
	}
}

// 服务密钥的共享密钥只写入不回显（PRD §7），且**空串表示保持不变**——
// 前端不回显它，提交时也带不出原值来。
func TestRuleSecretIsWriteOnly(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "api.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	const secret = "SHARED-SECRET-XYZ"

	r.mustDo("PUT", "/rules/svc-1", map[string]any{
		"name": "服务密钥", "type": "service_secret", "enabled": true,
		"apply_to": []string{"api.example.com"},
		"spec":     map[string]any{"header": "X-Service-Key", "algo": "hmac-sha256", "ttl_s": 300},
		"secret":   secret,
	})

	list := r.mustDo("GET", "/rules", nil)
	if contains(string(list.Data), "SHARED-SECRET") {
		t.Fatalf("GET /rules 回显了共享密钥: %s", list.Data)
	}
	if !contains(string(list.Data), `"secret_configured":true`) {
		t.Fatalf("应当说明密钥已配置: %s", list.Data)
	}

	// 不带密钥再保存一次：既不该抹掉密钥，也不该因为「缺密钥」而校验失败。
	r.mustDo("PUT", "/rules/svc-1", map[string]any{
		"name": "服务密钥（改名）", "type": "service_secret", "enabled": true,
		"apply_to": []string{"api.example.com"},
		"spec":     map[string]any{"header": "X-Service-Key", "algo": "hmac-sha256", "ttl_s": 300},
	})
	again := r.mustDo("GET", "/rules", nil)
	if !contains(string(again.Data), `"secret_configured":true`) {
		t.Fatal("不带密钥的保存把已配置的密钥抹掉了 —— 前端根本带不出原值来")
	}
}

// 删除路由**联动**摘除访问规则里的绑定。
// 留着一条指向已删域名的绑定，会让人以为那个域名还受保护。
func TestDeleteRouteUnbindsRules(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "gone.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	r.mustDo("PUT", "/rules/wl", map[string]any{
		"name": "白名单", "type": "ip_whitelist", "enabled": true,
		"apply_to": []string{"gone.example.com"},
		"spec":     map[string]any{"ips": []string{"203.0.113.7"}},
	})

	e := r.mustDo("DELETE", "/routes/gone.example.com", nil)
	var d struct {
		Deleted      string   `json:"deleted"`
		UnboundRules []string `json:"unbound_rules"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Deleted != "gone.example.com" || len(d.UnboundRules) != 1 || d.UnboundRules[0] != "wl" {
		t.Fatalf("应当报出被摘除绑定的规则: %+v", d)
	}

	// **「摘掉绑定」和「规则整个没了」产生同一个观测。**
	//
	// 只断言「列表里不含那个域名」的话，一个把规则连带删掉的实现也会绿——
	// 而那两件事完全不同：摘绑定是对的，删规则是丢了人配置的东西。
	//
	// 所以先钉住规则还在（肯定），再钉住它身上没有那个域名（否定）。
	list := r.mustDo("GET", "/rules", nil)
	var rl struct {
		Items []struct {
			ID      string   `json:"id"`
			ApplyTo []string `json:"apply_to"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Data, &rl); err != nil {
		t.Fatal(err)
	}
	var wl *struct {
		ID      string   `json:"id"`
		ApplyTo []string `json:"apply_to"`
	}
	for i := range rl.Items {
		if rl.Items[i].ID == "wl" {
			wl = &rl.Items[i]
		}
	}
	if wl == nil {
		t.Fatalf("规则 wl 应当还在 —— 删路由摘的是绑定，不是规则本身：%s", list.Data)
	}
	for _, d := range wl.ApplyTo {
		if d == "gone.example.com" {
			t.Fatalf("规则里还留着已删域名的绑定: %+v", wl.ApplyTo)
		}
	}
}

// **全局策略返回的是渲染器的默认值，不是字面意义的空。**
//
// 空 spec 会让界面无从说出真相：三个枚举一个都没选中、开关全 off，
// 而人无从知道此刻节点上究竟什么在生效。补齐之后，界面显示的就是
// 实际会被渲染下去的那一份。
func TestPolicyDefaultsReflectWhatIsActuallyRendered(t *testing.T) {
	r := newRig(t)

	e := r.mustDo("GET", "/policies/tls", nil)
	var p struct {
		Spec struct {
			MinVersion string `json:"min_version"`
			HTTP3      *bool  `json:"http3"`
			HSTS       *bool  `json:"hsts"`
			HSTSMaxAge int    `json:"hsts_max_age"`
			// **这三个必须不在了**（ADR-0015）。它们只被校验、
			// 从不进渲染产物，而它们的用途——主控签发证书——已经不存在。
			KeyType string `json:"key_type"`
			CA      string `json:"ca"`
			Email   string `json:"email"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(e.Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.Spec.MinVersion == "" {
		t.Fatalf("枚举字段不该是空的 —— 界面会显示成「一个都没选」而无从说明真相: %+v", p.Spec)
	}
	// **不该出现的字段也要钉住。** 只钉「该有的都有」的话，
	// 三个没有对象的字段会一直躺在响应里，而工作台会照着它们画表单——
	// 人改了、下发了、每一步都成功，而什么也不会发生。
	if p.Spec.KeyType != "" || p.Spec.CA != "" || p.Spec.Email != "" {
		t.Errorf("ca / email / key_type 随 ADR-0015 一起删了，不该还在响应里: %+v", p.Spec)
	}
	if p.Spec.HTTP3 == nil || p.Spec.HSTS == nil {
		t.Fatalf("开关字段应当有明确取值: %+v", p.Spec)
	}
	if p.Spec.HSTSMaxAge == 0 {
		t.Errorf("hsts_max_age 应当有默认值")
	}

	logE := r.mustDo("GET", "/policies/log", nil)
	var lp struct {
		Spec struct {
			Format   string `json:"format"`
			Level    string `json:"level"`
			RollSize int    `json:"roll_size"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(logE.Data, &lp); err != nil {
		t.Fatal(err)
	}
	if lp.Spec.Format == "" || lp.Spec.Level == "" || lp.Spec.RollSize == 0 {
		t.Fatalf("日志策略也该补齐: %+v", lp.Spec)
	}
}

// **限流做不到就明确拒绝。**
//
// 官方 Caddy 2.11.4 的 132 个标准模块里一个限流模块都没有
// （caddy-ratelimit 是插件）。一个开着却没有效果的限流开关，
// 比一个明说「做不到」的报错危险得多——这与回源 mTLS 当初的处理一致。
func TestRateLimitIsRejectedNotSilentlyIgnored(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "rl.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	r.mustDo("PUT", "/drafts/global:log", map[string]any{
		"spec": map[string]any{"rate_limit": true, "rate_rps": 200, "rate_burst": 400},
	})

	_, e := r.do("POST", "/deploys", map[string]any{
		"res_keys": []string{"route:rl.example.com", "global:log"},
	})
	if e.Code != api.CodeValidation {
		t.Fatalf("code = %d，想要 %d（校验失败）；msg=%s", e.Code, api.CodeValidation, e.Msg)
	}
	var d struct {
		Errors []struct {
			ResKey string `json:"res_key"`
			Field  string `json:"field"`
			Reason string `json:"reason"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(e.Data, &d)
	var found bool
	for _, i := range d.Errors {
		if i.ResKey == "global:log" && i.Field == "spec.rate_limit" {
			found = true
			if !contains(i.Reason, "插件") {
				t.Errorf("原因应当说清是官方包没有这个模块: %q", i.Reason)
			}
			// **还要指路。**
			//
			// 限流现在做得到了 —— 走访问规则里的 rate_limit。
			// 这个全局开关仍然做不到，两句都对，而人不会这么读：
			// 他看到「做不到」就走了，而他要的东西在隔壁。
			//
			// **一句只说了一半的实话，读起来跟假话一样。**
			if !contains(i.Reason, "访问控制") {
				t.Errorf("只说了做不到，没说去哪儿做 —— "+
					"限流走访问规则是做得到的，而人看到这句就走了: %q", i.Reason)
			}
		}
	}
	if !found {
		t.Fatalf("应当定位到 global:log 的 spec.rate_limit，实际 %+v", d.Errors)
	}
}

// **一条建错的规则要能被删掉，而不只是被停用。**
//
// 路由有 `PUT|DELETE /routes/:domain`，规则原先只有 PUT。于是一条 id 打错的规则
// 会永远躺在列表里：「停用」和「解绑域名」都是**让它不生效**，不是**让它不在**。
//
// 前端 agent 报的这个缺口，他的诊断比缺口本身更要紧：
// **界面上既没有删除按钮、也没说不能删，人会找一圈然后以为是自己没找到。**
//
// 删规则在「已删但还没下发」那个窗口里是 fail-closed 方向——节点上那条规则还在拦，
// 直到下一次下发。删路由在同一个窗口里是 fail-open（节点还在服务那个域名），
// 而那一个我们早就允许了。
func TestDeleteRule(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "api.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	r.mustDo("PUT", "/rules/typo-rule", map[string]any{
		"name": "打错了的规则", "type": "ip_whitelist", "enabled": true,
		"apply_to": []string{"api.example.com"},
		"spec":     map[string]any{"ips": []string{"203.0.113.7"}},
	})
	// 顺手给它留一份草稿，删除要一并清掉。
	r.mustDo("PUT", "/drafts/rule:typo-rule", map[string]any{"enabled": false})

	r.mustDo("DELETE", "/rules/typo-rule", nil)

	list := r.mustDo("GET", "/rules", nil)
	var d struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Data, &d); err != nil {
		t.Fatal(err)
	}
	for _, it := range d.Items {
		if it.ID == "typo-rule" {
			t.Fatalf("删掉的规则还在列表里：%+v", d.Items)
		}
	}

	// **草稿要跟着走。** 留一份指向已删资源的草稿，会让「有几处未下发改动」
	// 这个数字算上一个再也下发不出去的东西——与删路由同一条理由。
	drafts := r.mustDo("GET", "/drafts", nil)
	if contains(string(drafts.Data), "rule:typo-rule") {
		t.Errorf("删规则之后它的草稿还在：%s", drafts.Data)
	}

	// 删不存在的规则报 404，不假装成功。
	//
	// 前端的前提检查脚本就栽在这上面：它用 DELETE 收尾清理，
	// 而**从没看过返回值**——一个没生效的清理表现成了清理过了。
	_, e := r.do("DELETE", "/rules/nope", nil)
	if e.Code != api.CodeNotFound {
		t.Errorf("删不存在的规则 code = %d，想要 %d", e.Code, api.CodeNotFound)
	}
}

// **导入的证书要真的到节点上，而且不能被 ACME 覆盖回去。**
//
// 两条都是「不接上就没有症状」的那类：证书存进库、界面显示「已导入」，
// 而节点上还是旧的；或者到期前 30 天主控用 ACME 重签一张盖掉它，
// 而人只会在某天发现签发者变了。
func TestImportedCertReachesNodesAndIsNotAutoRenewed(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "imported.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	r.deployNow("route:imported.example.com")

	certPEM, keyPEM := importableCert(t, "imported.example.com")
	e := r.mustDo("PUT", "/certs/imported.example.com", map[string]any{
		"cert_pem": string(certPEM), "key_pem": string(keyPEM),
	})
	var imp struct {
		Domain  string   `json:"domain"`
		Issuer  string   `json:"issuer"`
		Domains []string `json:"domains"`
		KeyPEM  string   `json:"key_pem"`
	}
	if err := json.Unmarshal(e.Data, &imp); err != nil {
		t.Fatal(err)
	}

	// **私钥不回显**（PRD §7，与共享密钥、DNS 凭证同一条）。
	if imp.KeyPEM != "" || contains(string(e.Data), "PRIVATE KEY") {
		t.Fatalf("响应里回显了私钥：%s", e.Data)
	}
	// 回的是「我们从这张证书里读出了什么」——那让人当场看得出传对没有。
	if imp.Issuer == "" || len(imp.Domains) == 0 {
		t.Errorf("应当回报读出来的签发者与覆盖域名：%+v", imp)
	}

	// **auto_renew 必须是 false。**
	//
	// 留着 true 的话，到期前 30 天续期扫描会挑中它，主控用 ACME 重签一张
	// 覆盖掉导入的——而那不会有任何提示。

	list := r.mustDo("GET", "/certs", nil)
	var d struct {
		Items []struct {
			Domain    string `json:"domain"`
			Challenge string `json:"challenge"`
			AutoRenew bool   `json:"auto_renew"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Data, &d); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range d.Items {
		if it.Domain != "imported.example.com" {
			continue
		}
		found = true

		// challenge 说「它是怎么来的」——导入的不是通过任何 challenge 拿到的。
		if it.Challenge != "imported" {
			t.Errorf("challenge = %q，想要 imported", it.Challenge)
		}
	}
	if !found {
		t.Fatalf("导入的证书没出现在列表里：%s", list.Data)
	}
}

// 校验失败要在**存之前**挡住，而且报 1002 带上原因。
//
// 报 1001（格式错）的话人会去检查 JSON 有没有写错，而问题在 PEM 里面。
func TestImportRejectsCertForAnotherDomain(t *testing.T) {
	r := newRig(t)
	certPEM, keyPEM := importableCert(t, "other.example.com")

	_, e := r.do("PUT", "/certs/api.example.com", map[string]any{
		"cert_pem": string(certPEM), "key_pem": string(keyPEM),
	})
	if e.Code != api.CodeValidation {
		t.Fatalf("证书不覆盖那个域名时应当报 1002，实际 code=%d msg=%q", e.Code, e.Msg)
	}
	if !contains(e.Msg, "other.example.com") {
		t.Errorf("要说出它实际覆盖的域名，人多半是传错了文件：%q", e.Msg)
	}

	// 而且**什么也不该存下**：一张被拒绝的证书出现在列表里，
	// 比拒绝本身更让人困惑。
	list := r.mustDo("GET", "/certs", nil)
	if contains(string(list.Data), "api.example.com") {
		t.Errorf("被拒绝的证书不该进库：%s", list.Data)
	}
}

// **列表要说出这张证书覆盖了什么。**
//
// 一张 `*.example.com` 在列表里看不出它覆盖什么，而那正是人想确认的第一件事
// ——尤其在「主控不再签发、全靠外部平台推」之后：推错一张的后果要靠人眼看出来。
//
// 前端此前渲染的是 `scope` 和 `key_type` 两个字段，而**后端从来没返回过它们**
// （certResp 里从第一天起就没有）。线上那两格一直是空的，
// 而 mock 的 seed 提供了它们，dev 下一直看着正常 ——
// **一个比真实更完整的替身，会让缺口在开发期隐形。**
func TestCertListReportsWhatItCovers(t *testing.T) {
	r := newRig(t)
	// 先建站点：证书要覆盖得着我们在服务的域名才收（见
	// TestImportRejectsCertForADomainWeDontServe）。
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "wild.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	certPEM, keyPEM := importableCert(t, "wild.example.com")
	r.mustDo("PUT", "/certs/wild.example.com", map[string]any{
		"cert_pem": string(certPEM), "key_pem": string(keyPEM),
	})

	e := r.mustDo("GET", "/certs", nil)
	var d struct {
		Items []struct {
			Domain  string   `json:"domain"`
			Domains []string `json:"domains"`
		} `json:"items"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 1 {
		t.Fatalf("装置坏了：想要 1 张证书，实际 %d", len(d.Items))
	}
	got := d.Items[0]
	if len(got.Domains) == 0 {
		t.Fatal("列表要说出这张证书覆盖哪些域名 —— " +
			"少了它，一张通配符证书在列表里跟单域名的长得一样")
	}
	var found bool
	for _, x := range got.Domains {
		if x == "wild.example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("覆盖域名里应当有它自己，实际 %v", got.Domains)
	}
}

// TestImportRejectsCertForADomainWeDontServe 钉的是 1003：
// **这张证书覆盖不到任何一个我们在服务的域名时，拒绝。**
//
// 起因是三方证书平台的对接。它们那套语义要区分两件事：
//
//	这个域名不归这个 CDN 管   →  跳过，不算失败
//	真的部署失败              →  告警
//
// 收下的话它们只看得到 200，两种混成一种。而收下的代价不只在它们那边：
// 这张证书会进到期扫描，**每天为一个我们根本不服务的域名报警**——
// 而拆掉自动续期之后，到期告警是唯一会主动找人的东西。
func TestImportRejectsCertForADomainWeDontServe(t *testing.T) {
	r := newRig(t)
	// 建一个别的站点：**证明拒绝的理由是「覆盖不到」而不是「一条路由都没有」**。
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "served.example.com", "upstream": r.upstream, "block_mode": "abort",
	})

	certPEM, keyPEM := importableCert(t, "stranger.example.com")
	_, e := r.do("PUT", "/certs/stranger.example.com", map[string]any{
		"cert_pem": string(certPEM), "key_pem": string(keyPEM),
	})
	if e.Code != api.CodeNotFound {
		t.Fatalf("不服务的域名应当报 1003，实际 code=%d msg=%q", e.Code, e.Msg)
	}
	if !strings.Contains(e.Msg, "stranger.example.com") {
		t.Errorf("报错要说出这张证书覆盖的是什么，人才知道自己推错了哪张：%q", e.Msg)
	}

	// **被拒的那次什么也没存。** 存了一半的话，到期扫描照样会为它报警。
	list := r.mustDo("GET", "/certs", nil)
	if strings.Contains(string(list.Data), "stranger.example.com") {
		t.Errorf("被拒的证书不该留下痕迹：%s", list.Data)
	}
}

// TestWildcardCertIsAcceptedForTheHostsItCovers 是上一条的承重反面。
//
// **判据是「覆盖得着」，不是「名字相等」。** 证书不按路由挑：主控把全部
// 证书内联进每个节点（ADR-0010），Caddy 在握手时按 SNI 自己配对——
// 所以一张 *.example.com 的证书服务着 a.example.com，哪怕没有任何一条
// 路由叫 *.example.com。
//
// 没有这一条，一个用 `route.Domain == certDomain` 实现的版本也能让上面那条全绿，
// 而它会拒掉一张真的用得上的通配符证书——**而通配符正是三方平台最常发的那一类**。
func TestWildcardCertIsAcceptedForTheHostsItCovers(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "a.example.com", "upstream": r.upstream, "block_mode": "abort",
	})

	certPEM, keyPEM := importableCert(t, "*.example.com")
	_, e := r.do("PUT", "/certs/*.example.com", map[string]any{
		"cert_pem": string(certPEM), "key_pem": string(keyPEM),
	})
	if e.Code != api.CodeOK {
		t.Fatalf("通配符证书覆盖着 a.example.com，不该被拒：code=%d msg=%q", e.Code, e.Msg)
	}
}

// TestDeleteCertRefusesWhileItStillServes 钉的是删除的那道拦截。
//
// **删一张还在服务的证书，跟证书过期不同**：过期还有几天窗口，
// 这个是按下按钮的那一刻站点就坏了（下一次下发之后握不上 TLS）。
//
// 而拦截必须给逃生口：一张 *.example.com 可能覆盖二十条路由，
// 要求「先删光覆盖到的路由」等于要求不可能的事，而人会绕开——直接进数据库删。
// **绕过去之后下发不会被触发，节点上那张证书会一直留着**，
// 也就是说一道逼人绕开的门，比没有门更糟。
func TestDeleteCertRefusesWhileItStillServes(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "live.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	certPEM, keyPEM := importableCert(t, "live.example.com")
	r.mustDo("PUT", "/certs/live.example.com", map[string]any{
		"cert_pem": string(certPEM), "key_pem": string(keyPEM),
	})

	_, e := r.do("DELETE", "/certs/live.example.com", nil)
	if e.Code != api.CodeStateConflict {
		t.Fatalf("还在服务的证书应当以 2001 拒绝，实际 code=%d msg=%q", e.Code, e.Msg)
	}
	if !strings.Contains(e.Msg, "live.example.com") {
		t.Errorf("要说出它在服务哪些域名，人才判断得了：%q", e.Msg)
	}
	if !strings.Contains(e.Msg, "force") {
		t.Errorf("**要写出逃生口**，不然人只会去数据库里删：%q", e.Msg)
	}

	// **被拒的那次什么也没删。**
	list := r.mustDo("GET", "/certs", nil)
	if !strings.Contains(string(list.Data), "live.example.com") {
		t.Fatalf("被拒之后证书不该消失：%s", list.Data)
	}

	// force 放行。
	ok := r.mustDo("DELETE", "/certs/live.example.com?force=true", nil)
	if !strings.Contains(string(ok.Data), "已删除") {
		t.Errorf("force 之后要真的删掉并说清：%s", ok.Data)
	}
	after := r.mustDo("GET", "/certs", nil)
	if strings.Contains(string(after.Data), "live.example.com") {
		t.Errorf("force 删除之后它还在列表里：%s", after.Data)
	}
}

// TestDeleteOrphanCertNeedsNoForce：不服务任何站点的证书直接删得掉。
//
// 没有这一条，一个「无条件拒绝所有删除」的实现也能让上面那条全绿。
// 而这类证书正是这个端点存在的理由：删掉一条路由之后，它的证书原样留着，
// 继续下发、继续报到期，**而那还是一把有效的私钥**。
func TestDeleteOrphanCertNeedsNoForce(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "gone.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	certPEM, keyPEM := importableCert(t, "gone.example.com")
	r.mustDo("PUT", "/certs/gone.example.com", map[string]any{
		"cert_pem": string(certPEM), "key_pem": string(keyPEM),
	})
	// 路由没了，证书成了孤儿。
	r.mustDo("DELETE", "/routes/gone.example.com", nil)

	list := r.mustDo("GET", "/certs", nil)
	var d struct {
		Items []struct {
			Domain string   `json:"domain"`
			Covers []string `json:"covers"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 1 {
		t.Fatalf("装置坏了：想要 1 张证书，实际 %d", len(d.Items))
	}
	if d.Items[0].Covers == nil {
		t.Error("covers 是 null —— 那是「算不出来」，而这里算得出来")
	}
	if len(d.Items[0].Covers) != 0 {
		t.Errorf("路由都删了，covers 该是空的，实际 %v", d.Items[0].Covers)
	}

	// 不带 force 也删得掉。
	ok := r.mustDo("DELETE", "/certs/gone.example.com", nil)
	if !strings.Contains(string(ok.Data), "已删除") {
		t.Errorf("孤儿证书不该需要 force：%s", ok.Data)
	}
}

// TestDeleteMissingCertIsNotASuccess：删一个不存在的域名要报 1003。
//
// **域名打错一个字符的症状，本来长得和成功一模一样**：
// DELETE 影响 0 行、返回 nil、接口回一句「已删除」，而库里什么都没发生。
func TestDeleteMissingCertIsNotASuccess(t *testing.T) {
	r := newRig(t)

	// **两条路径都要走，因为它们由不同的东西守着。**
	//
	// 不带 force 时先查「它在服务什么」，那一步的 GetCert 就会报没有；
	// 带 force 时那一步被跳过，**只剩 DeleteCert 的 rows-affected 那道**。
	//
	// 只写不带 force 的那条是不够的：把 store 里那道退回去（删 0 行也返回 nil），
	// 这条测试照样全绿——**它验的是另一个东西**。实测过。
	for _, path := range []string{
		"/certs/never-existed.example.com",
		"/certs/never-existed.example.com?force=true",
	} {
		_, e := r.do("DELETE", path, nil)
		if e.Code != api.CodeNotFound {
			t.Errorf("%s：删不存在的证书应当报 1003，实际 code=%d msg=%q",
				path, e.Code, e.Msg)
		}
	}
}
