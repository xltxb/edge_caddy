package e2e_test

import (
	"encoding/json"
	"net/http"
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
		Phase       string `json:"phase"`
		TargetCount int    `json:"target_count"`
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
