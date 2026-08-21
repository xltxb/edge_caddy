package e2e_test

import (
	"encoding/json"
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

	// 签发（测试签发者用内部 CA 代替真 ACME）。
	r.mustDo("POST", "/certs/secure.example.com/renew", nil)
	before := r.waitCert("secure.example.com", func(c certItem) bool { return c.DaysLeft > 0 },
		"证书被签发出来")

	// 主控账面上有了，但**还没下发**——节点上一张都没有。
	if before.LoadedNodes != 0 {
		t.Fatalf("还没下发时不该有节点回执，实际 loaded=%d", before.LoadedNodes)
	}
	if before.ExpectedNodes != 1 || len(before.MissingNodes) != 1 {
		t.Fatalf("应当报出缺哪一台：%+v", before)
	}
	if before.Challenge != "dns-01" {
		// HTTP-01 在这个系统里不成立：域名按权重只解析到部分节点，
		// 轮换外的节点完不成校验（ADR-0001）。
		t.Errorf("challenge = %q，想要 dns-01", before.Challenge)
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

// 续期检查会把快到期的排进队列。
func TestRenewCheckQueuesExpiringCerts(t *testing.T) {
	r := newRig(t)
	e := r.mustDo("POST", "/certs/renew-check", nil)
	var d struct {
		Accepted bool `json:"accepted"`
		Queued   int  `json:"queued"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if !d.Accepted {
		t.Fatal("批量检查应当被接受")
	}
	if d.Queued != 0 {
		t.Errorf("一张证书都没有时不该排队，实际 %d", d.Queued)
	}
}

func TestRenewUnknownDomainIsNotFound(t *testing.T) {
	r := newRig(t)
	_, e := r.do("POST", "/certs/nope.example.com/renew", nil)
	if e.Code == 0 {
		t.Fatal("对不存在的证书续期应当失败")
	}
}

// 证书跟着路由走：下发之后，路由域名自动拿到证书，不需要人手动点签发。
func TestCertsFollowRoutesAutomatically(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	if len(r.certs()) != 0 {
		t.Fatal("一开始不该有证书")
	}

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "auto.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	r.deployNow("route:auto.example.com")

	got := r.waitCert("auto.example.com", func(c certItem) bool { return c.DaysLeft > 0 },
		"路由域名自动拿到证书")
	if got.Issuer == "" {
		t.Error("应当记下签发者，证书页要显示它")
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
			KeyType    string `json:"key_type"`
			CA         string `json:"ca"`
			HTTP3      *bool  `json:"http3"`
			HSTS       *bool  `json:"hsts"`
			HSTSMaxAge int    `json:"hsts_max_age"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(e.Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.Spec.MinVersion == "" || p.Spec.KeyType == "" || p.Spec.CA == "" {
		t.Fatalf("枚举字段不该是空的 —— 界面会显示成「一个都没选」而无从说明真相: %+v", p.Spec)
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
