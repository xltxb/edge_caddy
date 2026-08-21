package api

import (
	"testing"

	"github.com/xltxb/edge_caddy/internal/store"
)

// 「结束了」的定义是「全部目标都回报了，且没有还会再动的」。
//
// 这几条锁住的是前端在 mock 上撞出来的那个事实：**6/6 并不等于结束了**。
// 重试中的节点已经回报过一次失败，但它那一行还会变；把它算成终态会让
// 确认弹层提前落定，而用户以为下发已经收尾。
func TestDeployPhase(t *testing.T) {
	ok := func(node string) store.DeployResult {
		return store.DeployResult{Node: node, State: "ok", Detail: "31ms"}
	}
	failTerminal := func(node string) store.DeployResult {
		return store.DeployResult{Node: node, State: "fail", Detail: "unknown handler", Retrying: false}
	}
	failRetrying := func(node string) store.DeployResult {
		return store.DeployResult{Node: node, State: "fail", Detail: "deadline exceeded", Retrying: true}
	}

	cases := []struct {
		name    string
		targets int
		results []store.DeployResult
		want    string
	}{
		{"一个都还没回报", 3, nil, "running"},
		{"回报了一部分", 3, []store.DeployResult{ok("a")}, "running"},
		{"全部成功", 2, []store.DeployResult{ok("a"), ok("b")}, "done"},
		{"有终态失败也算结束", 2, []store.DeployResult{ok("a"), failTerminal("b")}, "done"},
		{"6/6 但有节点重试中 —— 没结束", 2, []store.DeployResult{ok("a"), failRetrying("b")}, "running"},
		{"目标数未知时不敢说结束", 0, []store.DeployResult{ok("a")}, "running"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deployPhase(c.targets, c.results); got != c.want {
				t.Errorf("phase = %q，想要 %q", got, c.want)
			}
		})
	}
}
