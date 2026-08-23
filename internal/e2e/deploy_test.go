package e2e_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/api"
)

// TestSliceOne 是 issue #18 的验收标准本身，不是它的近似。
//
// 一条竖线跑通：签发接入 Token → 节点接入并取得隧道证书 → 心跳上线 →
// 新建一条路由 → 下发 → Agent 应用到本机 Caddy → **一条真请求被代理到上游**。
//
// 它一次验掉四件事：接入、隧道、渲染、热重载。之所以不拆成「接入」和「下发」
// 两个测试：接入单独拿出来只能断言「列表里多一行绿灯」，而绿灯是最容易造假的东西。
func TestSliceOne_EnrollThenDeployThenTrafficFlows(t *testing.T) {
	r := newRig(t)

	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "api.example.com", "upstream": r.upstream,
		"block_mode": "abort", "compress": true, "body_max": "5MB",
	})

	e := r.mustDo("POST", "/deploys", map[string]any{
		"res_keys": []string{"route:api.example.com"},
	})
	var d struct {
		DeployID   int64    `json:"deploy_id"`
		CfgVersion string   `json:"cfg_version"`
		Targets    []string `json:"targets"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Targets) != 1 || d.Targets[0] != "node-hk-01" {
		t.Fatalf("目标节点 = %v，想要 [node-hk-01]", d.Targets)
	}

	// 逐节点结果：不允许「整体成功/失败」的黑盒（PRD §7）。
	detail := r.mustDo("GET", "/deploys/"+itoa(d.DeployID), nil)
	var dd struct {
		Phase   string `json:"phase"`
		OKCount int    `json:"ok_count"`
		Results []struct {
			Node     string `json:"node"`
			State    string `json:"state"`
			Detail   string `json:"detail"`
			Retrying bool   `json:"retrying"`
		} `json:"results"`
	}
	if err := json.Unmarshal(detail.Data, &dd); err != nil {
		t.Fatal(err)
	}
	if dd.OKCount != 1 {
		t.Fatalf("成功节点数 = %d，想要 1；结果=%+v", dd.OKCount, dd.Results)
	}
	if len(dd.Results) != 1 || dd.Results[0].State != "ok" {
		t.Fatalf("逐节点结果 = %+v", dd.Results)
	}
	if dd.Results[0].Detail == "" {
		t.Error("成功的节点应当回报热重载耗时——控制台上那个「31ms」来自这里")
	}
	// 本切片没有重试队列，报 true 就是承诺一件不会发生的事。
	if dd.Results[0].Retrying {
		t.Error("本切片不实现重试队列，retrying 必须为 false")
	}

	// ==== 验收：一条真请求进 Caddy，按路由回源，拿到上游的响应 ====
	code, body := r.curlVia("api.example.com")
	if code != 200 || body != "UPSTREAM OK" {
		t.Fatalf("经边缘节点回源得到 %d %q，想要 200 \"UPSTREAM OK\"", code, body)
	}
}

// 下发落定后基线前进，且被下发的草稿被清空。
func TestDeployAdvancesBaselineAndClearsDraft(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "b.example.com", "upstream": "127.0.0.1:1", "block_mode": "abort",
	})
	// 草稿：把回源地址改到真上游。effective = merge(live, draft)。
	r.mustDo("PUT", "/drafts/route:b.example.com", map[string]any{"upstream": r.upstream})

	before := r.baseline()
	e := r.mustDo("POST", "/deploys", map[string]any{"res_keys": []string{"route:b.example.com"}})
	var d struct {
		CfgVersion string `json:"cfg_version"`
	}
	_ = json.Unmarshal(e.Data, &d)

	after := r.baseline()
	if after == before {
		t.Fatal("下发成功后基线应当前进")
	}
	if after != d.CfgVersion {
		t.Fatalf("基线 = %q，想要本次下发的 %q", after, d.CfgVersion)
	}

	// 草稿被清空：勾选下发的那份改动已经合入基线，不再是草稿。
	drafts := r.mustDo("GET", "/drafts", nil)
	var dr struct {
		Items map[string]json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(drafts.Data, &dr)
	if _, still := dr.Items["route:b.example.com"]; still {
		t.Error("已下发的草稿应当被清空")
	}

	// 草稿真的生效了：请求打到的是草稿里那个上游，不是 live 里的 127.0.0.1:1。
	if code, body := r.curlVia("b.example.com"); code != 200 || body != "UPSTREAM OK" {
		t.Fatalf("草稿里的回源地址没生效：得到 %d %q", code, body)
	}
}

// 校验不过时**一个节点都不被触达**，也不产生下发记录。
func TestValidationFailureTouchesNoNode(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "c.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	// 把回源地址改成非法值。
	r.mustDo("PUT", "/drafts/route:c.example.com", map[string]any{"upstream": "没有端口"})

	status, e := r.do("POST", "/deploys", map[string]any{"res_keys": []string{"route:c.example.com"}})
	if status != http.StatusOK {
		t.Fatalf("业务失败应当走 http 200，实际 %d", status)
	}
	if e.Code != api.CodeValidation {
		t.Fatalf("code = %d，想要 %d（校验失败）", e.Code, api.CodeValidation)
	}

	// 结构化到字段，前端才能让那个输入框转红。
	var d struct {
		Errors []struct {
			ResKey string `json:"res_key"`
			Field  string `json:"field"`
			Reason string `json:"reason"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("校验失败的 data 应当带 errors: %s", e.Data)
	}
	if len(d.Errors) == 0 || d.Errors[0].Field != "upstream" {
		t.Fatalf("errors = %+v，想要定位到 upstream 字段", d.Errors)
	}

	if n := r.countDeploys(); n != 0 {
		t.Fatalf("校验不过时不该产生下发记录，实际有 %d 条", n)
	}
}

// 没有在线节点时下发是个无操作。静默成功会让人以为配置生效了，
// 而实际上一台机器都没收到。
func TestDeployWithNoOnlineNodesIsRefused(t *testing.T) {
	r := newRig(t)
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "d.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	_, e := r.do("POST", "/deploys", map[string]any{"res_keys": []string{"route:d.example.com"}})
	if e.Code != api.CodeStateConflict {
		t.Fatalf("code = %d，想要 %d（状态冲突）；msg=%s", e.Code, api.CodeStateConflict, e.Msg)
	}
}

// 接入 Token 用后即失效：同一条安装命令被跑两遍不会产生第二个身份。
func TestEnrollTokenIsSingleUse(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	// 拿同一个 Token 换一台机器再接一次——应当失败。
	dir := t.TempDir()
	r.startAgent("node-hk-02", token, dir)
	time.Sleep(700 * time.Millisecond)

	if r.isOnline("node-hk-02") {
		t.Fatal("已被使用的 Token 不该让第二台机器接入")
	}
}

// 重启后凭已落盘的证书用 mTLS 重连，不需要新 Token（ADR-0009）。
func TestAgentReconnectsWithStoredCertificateNotToken(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")

	dir := t.TempDir()
	cancel := r.startAgent("node-hk-01", token, dir)
	r.waitOnline("node-hk-01")

	cancel() // 模拟 Agent 重启
	time.Sleep(300 * time.Millisecond)

	// 关键：这次**不带 Token**。能连上说明用的是落盘的隧道证书。
	r.startAgent("node-hk-01", "", dir)
	r.waitOnline("node-hk-01")
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// 轮询降级要真的能用：契约 §2 承诺 WS 断线时降级为 2s 轮询 GET /deploys/:id，
// 且它的字段与 deploy_progress 帧一一对应。
//
// 这条锁住的是「结果一到就落库」——攒到最后再写会让轮询在整个下发过程中
// 什么都看不到，降级路径就成了摆设，而那恰恰是用户最需要被告知的时刻。
func TestDeployDetailMirrorsProgressFrames(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "poll.example.com", "upstream": r.upstream, "block_mode": "abort",
	})
	e := r.mustDo("POST", "/deploys", map[string]any{"res_keys": []string{"route:poll.example.com"}})
	var d struct {
		DeployID int64 `json:"deploy_id"`
	}
	_ = json.Unmarshal(e.Data, &d)

	detail := r.mustDo("GET", "/deploys/"+itoa(d.DeployID), nil)
	var dd struct {
		Phase       string   `json:"phase"`
		TargetCount int      `json:"target_count"`
		Targets     []string `json:"targets"`
		Results     []struct {
			Node     string `json:"node"`
			State    string `json:"state"`
			Detail   string `json:"detail"`
			Retrying bool   `json:"retrying"`
		} `json:"results"`
	}
	if err := json.Unmarshal(detail.Data, &dd); err != nil {
		t.Fatal(err)
	}

	if dd.TargetCount != 1 {
		t.Fatalf("target_count = %d，想要 1；没有它就判断不出「结束了没有」", dd.TargetCount)
	}
	// targets 必须给出**是哪几个**节点，不只是几个。
	// 用户在下发进行中刷新页面时，前端手上没有 POST /deploys 那次响应，
	// 只有从这里读回来才画得出「待下发」的那几行——而「还有谁没回来」
	// 正是断线降级时最需要看见的信息。
	if len(dd.Targets) != 1 || dd.Targets[0] != "node-hk-01" {
		t.Fatalf("targets = %v，想要 [node-hk-01]", dd.Targets)
	}
	if dd.Phase != "done" {
		t.Fatalf("phase = %q，想要 done", dd.Phase)
	}
	// 与 deploy_progress 帧同构：node / state / detail / retrying 四个字段都要在，
	// 前端的 PushProgress 组件两条数据源共用一套渲染。
	if len(dd.Results) != 1 {
		t.Fatalf("results = %+v", dd.Results)
	}
	got := dd.Results[0]
	if got.Node == "" || got.State == "" || got.Detail == "" {
		t.Errorf("轮询返回的字段不完整: %+v", got)
	}
}

// 下发成功后，**live 必须变成下发的样子**。
//
// 这条是补的回归测试：先前 Deploy 清空了草稿、推到了节点，却从没把合并结果
// 写回 proxy_routes。于是节点上跑着新配置而真相源里还是旧值，下一次下发会把
// 旧值推回去——现象是「我明明改过、也下发成功了，怎么又变回去了」，
// 中间没有任何报错。
//
// 已有的测试没抓到它，因为它们只验了「节点上生效了」和「草稿清空了」。
// 那两件事在 bug 存在时**也是真的**。
func TestDeployCommitsDraftIntoLive(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("POST", "/routes", map[string]any{
		"domain": "live.example.com", "upstream": "127.0.0.1:1111",
		"block_mode": "abort", "body_max": "64MB",
	})
	r.mustDo("PUT", "/drafts/route:live.example.com", map[string]any{
		"upstream": r.upstream, "body_max": "8MB",
	})
	r.deployNow("route:live.example.com")

	routes := r.mustDo("GET", "/routes", nil)
	var d struct {
		Items []struct {
			Domain   string `json:"domain"`
			Upstream string `json:"upstream"`
			BodyMax  string `json:"body_max"`
			Version  int    `json:"version"`
		} `json:"items"`
	}
	if err := json.Unmarshal(routes.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 1 {
		t.Fatalf("items = %+v", d.Items)
	}
	got := d.Items[0]
	if got.Upstream != r.upstream || got.BodyMax != "8MB" {
		t.Fatalf("下发之后 live = %+v，想要草稿里的值（upstream=%s body_max=8MB）——"+
			"否则下一次下发会把旧值推回去", got, r.upstream)
	}
	if got.Version == 0 {
		t.Error("下发之后版本号应当前进，0 表示「尚未下发到任何节点」")
	}

	// 再下发一次（不带草稿）：配置必须保持不变，而不是回到 127.0.0.1:1111。
	r.deployNow()
	if code, body := r.curlVia("live.example.com"); code != 200 || body != "UPSTREAM OK" {
		t.Fatalf("空下发之后配置被推回了旧值：得到 %d %q", code, body)
	}
}

// **一份没有底子的草稿，下发时要被拦下来——而不是连人写的东西一起吞掉。**
//
// 草稿是「在已有资源上的改动」（Partial）。而 effective 里三个循环都是
// 「遍历 live、有草稿就套上去」，所以一份没有对应 live 资源的草稿会被**静默跳过**。
// 随后 DeleteDrafts 按 res_keys 无条件删。
//
// 修之前实测的完整过程：
//
//	写草稿 rule:brand-new  → GET /drafts 看得到，界面显示「待下发」
//	预览                    → 完全不提它，validation.ok = true
//	下发                    → 返回成功，有 cfg_version 和 deploy_id
//	之后                    → 草稿没了，GET /rules 是空的
//
// **成功的假象里最贵的一种：它同时是数据丢失。**
//
// 修法是把它接到已有的校验通道上，而不是造一个新的错误类型：预览的
// validation.ok 转假、下发用 1002 拒绝执行——**而下发被拒，DeleteDrafts
// 就跑不到，那份草稿因此活下来**。人还能把它改对。
//
// **第三条断言（草稿还在）在今天的实现里是骑在第二条上的**：正因为下发被拒，
// 删草稿那一步才跑不到。探针也证实了这点——破坏拦截时红的是第二条，
// 第三条根本没跑到。
//
// 留着它不是为了今天，是为了**「报了警但继续做」**那种改法：哪天有人觉得
// 「孤儿草稿只该提醒、不该挡住整次下发」，第二条会跟着改，而第三条会拦住他——
// 提醒完照样把人写的东西删掉，比什么都不提醒更坏。
func TestOrphanDraftIsRefusedNotSwallowed(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgent("node-hk-01", token, t.TempDir())
	r.waitOnline("node-hk-01")

	r.mustDo("PUT", "/drafts/rule:brand-new", map[string]any{
		"name": "新规则", "type": "ip_whitelist", "enabled": true,
		"apply_to": []string{}, "spec": map[string]any{"ips": []string{"1.2.3.4"}},
	})

	// 一、预览必须说出来，并且点名是哪个 res_key。
	prev := r.mustDo("POST", "/deploys/preview",
		map[string]any{"res_keys": []string{"rule:brand-new"}})
	var p struct {
		Validation struct {
			OK     bool `json:"ok"`
			Errors []struct {
				ResKey string `json:"res_key"`
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"validation"`
	}
	if err := json.Unmarshal(prev.Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.Validation.OK {
		t.Fatal("预览说没问题 —— 而这次下发什么也不会发生，草稿还会被删掉")
	}
	var named bool
	for _, e := range p.Validation.Errors {
		if e.ResKey == "rule:brand-new" {
			named = true
		}
	}
	if !named {
		t.Errorf("校验问题里要点名是哪个 res_key，实际 %+v", p.Validation.Errors)
	}

	// 二、下发要被拒（1002），不是「成功但什么也没做」。
	status, e := r.do("POST", "/deploys", map[string]any{"res_keys": []string{"rule:brand-new"}})
	if status != http.StatusOK || e.Code != api.CodeValidation {
		t.Fatalf("下发应当以 1002 拒绝，实际 http=%d code=%d msg=%s", status, e.Code, e.Msg)
	}

	// 三、**草稿必须还在。** 前两条都过而这条不过，是最坏的结果：
	// 人看到了错误提示，回头却发现自己写的东西已经没了。
	after := r.mustDo("GET", "/drafts", nil)
	if !strings.Contains(string(after.Data), "rule:brand-new") {
		t.Fatalf("下发被拒之后草稿必须还在，人才有机会把它改对：%s", after.Data)
	}
}

// **隧道走 HTTP 面那条路时，一切照旧。**
//
// 这条路存在的理由是灰度上撞到的：主控域名挂在 CDN 后面，而 CDN 只转发
// 80/443——隧道那个独立端口的包根本到不了主控。节点装完一切正常，
// 然后**永远不出现在控制台里**，而那台机器上没有任何东西说得出为什么。
//
// 换成 `wss://<主控域名>/api/v1/tunnel` 之后，隧道在中间设施眼里
// 就是一条平平无奇的 443 连接。
//
// **而里层一个字节都没改**：同一套 mTLS、同一个内部 CA、同一个 CA pin、
// 同一份 gRPC。节点身份仍然是客户端证书（ADR-0009），CDN 只看得见外层。
// 那一点是承重的：下发走内联证书（ADR-0010），
// **每个客户域名的私钥都在这条隧道里传**。
//
// 所以这条测试不满足于「连上了」——**接入、心跳、下发、真的过流量**
// 四步全走一遍。只验第一步的话，一个握手成功而数据面不通的实现照样全绿，
// 而那正是「机制建好了，没接到最该接的那个输出上」的形状。
func TestTunnelOverHTTPCarriesEverything(t *testing.T) {
	r := newRig(t)
	token, _ := r.issueToken("node-hk-01")
	r.startAgentOverWS("node-hk-01", token, t.TempDir())

	// 一、接入：节点得真的上线，而不只是 TCP 连上了。
	r.waitOnline("node-hk-01")

	// 二、下发：配置得真的到节点上。
	r.mustDo("POST", "/routes", map[string]any{
		"domain": "ws.example.com", "upstream": r.upstream,
		"block_mode": "abort", "body_max": "64MB",
	})
	r.deployNow()

	// 三、数据面：请求得真的打到源站。
	//
	// **这一步是这条测试的重点。** 隧道只是控制面，而「控制面通了」
	// 与「节点上的 Caddy 真的按新配置在服务」是两件事——
	// 前者绿而后者红，正是这一天反复撞见的那个形状。
	if code, body := r.curlVia("ws.example.com"); code != 200 || body != "UPSTREAM OK" {
		t.Fatalf("经 HTTP 面接入的节点没能正常服务：得到 %d %q", code, body)
	}

	// 四、隧道端点不吃会话，但也不因此变成一个开放的入口。
	//
	// 认证在**里层**那次 mTLS 握手里（ADR-0009）。所以：不带 WebSocket
	// 升级头的普通 GET 不该被当成隧道处理。
	status, e := r.do("GET", "/tunnel", nil)
	if status == http.StatusOK {
		t.Error("普通 GET /tunnel 不该返回 200 —— 它只接受 WebSocket 升级")
	}
	// 而且要按契约回信封、把话说清。gorilla 默认写的是纯文本「Bad Request」，
	// 那句话对一个把 wss:// 写成 https:// 的人毫无帮助——
	// 他会去查请求体，而这个端点根本没有请求体。
	if !strings.Contains(e.Msg, "WebSocket") {
		t.Errorf("拒绝的理由要说清是什么请求才对，实际 msg=%q", e.Msg)
	}
}
