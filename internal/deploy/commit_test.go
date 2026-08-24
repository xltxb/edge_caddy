package deploy_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// **commit 失败时，草稿必须活着，基线不能前进，且有人被告知。**
//
// 此前 commit 失败只记日志，流水线照走：基线前进、草稿被删。结果是
// 节点上跑着新配置、live 表还是旧值、草稿没了、界面说成功 ——
// 下一次任何下发把旧值推回去，而这次连草稿都找不回来（issue #31）。
// effective 的注释把这个形状叫「成功的假象里最贵的一种：它同时是数据丢失」。
func TestCommitFailureKeepsDraftsAndBaseline(t *testing.T) {
	p := newFakePusher("node-1")
	s, st := newSched(t, p)
	ctx := context.Background()

	const resKey = "route:api.example.com"
	if err := st.PutDraft(ctx, resKey,
		json.RawMessage(`{"upstream":"127.0.0.2:9090"}`), "tester"); err != nil {
		t.Fatal(err)
	}
	s.SetCommitFault(errors.New("注入：合入基线失败"))

	res, issues, err := s.Deploy(ctx, "tester", []string{resKey})
	if err != nil || len(issues) > 0 {
		t.Fatalf("下发本身不该失败（推送是成功的）：err=%v issues=%v", err, issues)
	}
	if res.OKCount != 1 {
		t.Fatalf("装置坏了：节点该收到配置，ok=%d", res.OKCount)
	}

	// 一、草稿活着 —— 它是此刻唯一还留着用户改动的地方。
	drafts, err := st.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	alive := false
	for _, d := range drafts {
		if d.ResKey == resKey {
			alive = true
		}
	}
	if !alive {
		t.Error("commit 失败后草稿被删了 —— 用户的改动哪儿都不在了")
	}

	// 二、基线不前进 —— 前进等于宣称「live 渲染出来就是这一版」，而那是假的。
	// 节点比基线新会被漂移检测标出来，那个标是真话。
	baseline, err := st.Baseline(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if baseline == res.CfgVersion {
		t.Error("commit 失败后基线还是前进了 —— live 里没有这一版的内容")
	}

	// 三、有人被告知 —— 只进 Error 日志的话，没人会在出事前发现。
	events, err := st.RecentEvents(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	told := false
	for _, e := range events {
		if strings.Contains(e.Msg, "合入基线失败") && strings.Contains(e.Msg, "草稿") {
			told = true
		}
	}
	if !told {
		t.Error("commit 失败没有写事件 —— 「节点是新的、库是旧的」这件事只有日志知道")
	}
}
