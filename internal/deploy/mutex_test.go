package deploy_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/pki"
)

// TestConcurrentDeploysDoNotInterleave：两次下发不会交错推送。
//
// Deploy 没有互斥（issue #32）。两个人（或一个人点两下、或人点的同时证书
// 导入触发了一次）同时下发时，两份配置会交错推到各个节点上——**节点的终态
// 取决于每台机器上谁后到**，而两次下发的记录都会说成功。
//
// Retries().CancelAll() 挡不住这个：它管的是补推，而首轮推送不经过它。
//
// 判据是**每台节点最后拿到的那份配置，与基线是同一份**。交错时这两者会
// 对不上，而界面上两次下发都是绿的——「成功的假象」，与 #31 同一族。
func TestConcurrentDeploysDoNotInterleave(t *testing.T) {
	p := newFakePusher("node-a", "node-b", "node-c")
	p.delay = 150 * time.Millisecond // 推各节点是并行的，延迟要长过下面那个错开量
	s, st := newSched(t, p)
	ctx := context.Background()

	// **两份不同的配置，错开进来。**
	//
	// 两个 goroutine 各自「先写草稿再下发」的话，它们很可能读到同一份草稿、
	// 算出同一个版本号——那时任何实现都是绿的，测试什么也没证明。
	// 这里让第一次推到一半时，第二次才带着另一份配置进来。
	const resKey = "route:api.example.com"
	deployWith := func(up string) {
		if err := st.PutDraft(ctx, resKey,
			json.RawMessage(`{"upstream":"`+up+`"}`), "tester"); err != nil {
			t.Error(err)
		}
		_, _, _ = s.Deploy(ctx, "tester", []string{resKey})
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); deployWith("127.0.0.101:9001") }()

	time.Sleep(30 * time.Millisecond) // 第一次还在推的时候，第二次进来
	deployWith("127.0.0.102:9002")
	wg.Wait()
	s.Retries().Wait()

	// **两次下发不该同时在往节点上写。**
	//
	// 谁先谁后不要紧，同时在写才是问题：那时每台机器的终态取决于它自己
	// 那一侧谁后到，而两次下发的记录都会说成功——「成功的假象」，
	// 与 #31 同一族。
	if a, b, yes := p.overlappingVersions(); yes {
		t.Errorf("%s 与 %s 的推送在时间上重叠了 —— 两次下发交错，"+
			"节点终态取决于每台机器上谁后到", a, b)
	}

	// 而终态要与基线一致：交错时这两者会对不上。
	baseline, err := st.Baseline(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"node-a", "node-b", "node-c"} {
		if got := p.lastVersionFor(node); got != baseline {
			t.Errorf("%s 上跑的是 %s，而基线是 %s", node, got, baseline)
		}
	}
}

// TestRenewalDoesNotInterleaveWithADeploy：续期循环也要排在同一条队里。
//
// #32 给 Deploy 加了 deployMu，而**这批改动里我自己新开的第二条推送路径
// （#39 的续期循环）没有拿它**——`renewUpstreamCerts` 整份渲染、逐节点 Push，
// 一个字都没锁。
//
// 于是 #32 的竞态原样回来了，只是发起方从「第二个人」换成了定时器：
// 节点上 pushMu 只保证一次应用一份配置，**谁后到谁生效**。若续期推的那份
// （旧基线）后到，节点跑旧配置，而 Deploy 已经把该节点的 cfg_version 写成新版
// ——ADR-0002 的漂移只比版本号，界面显示「全部一致」，实际不是。
//
// 判据与 #32 那条一样：两条推送路径的窗口不该重叠。
func TestRenewalDoesNotInterleaveWithADeploy(t *testing.T) {
	p := newFakePusher("node-a", "node-b", "node-c")
	p.delay = 150 * time.Millisecond
	s, st := newSched(t, p)
	ctx := context.Background()

	ca, err := pki.GenerateCA(pki.KindUpstream)
	if err != nil {
		t.Fatal(err)
	}
	s.UpstreamCA = ca
	s.Render.UpstreamClientCert = "/etc/caddy/upstream/client.crt"
	s.Render.UpstreamClientKey = "/etc/caddy/upstream/client.key"

	// 先立起基线，续期循环才有东西可推。
	if _, _, err := s.Deploy(ctx, "abiu", []string{"route:api.example.com"}); err != nil {
		t.Fatal(err)
	}
	p.forget()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.RunUpstreamRenewal(runCtx, 20*time.Millisecond)

	// 续期跑起来之后，人点一次下发。
	time.Sleep(30 * time.Millisecond)
	if err := st.PutDraft(ctx, "route:api.example.com",
		json.RawMessage(`{"upstream":"127.0.0.77:7777"}`), "tester"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Deploy(ctx, "tester", []string{"route:api.example.com"}); err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(200 * time.Millisecond)

	if a, b, yes := p.overlappingVersions(); yes {
		t.Errorf("%s 与 %s 的推送在时间上重叠了 —— 续期循环没有排在 deployMu 那条队里，"+
			"#32 的竞态换了个发起方又回来了", a, b)
	}
}
