package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestServeShutsDownGracefully：收到信号之后，关停动作真的发生。
//
// 主控原先走 gin 的 `srv.Run(addr)`——它是 ListenAndServe，没有关停钩子，
// 出错就 os.Exit(1)。于是 `defer st.Close()`、`defer tun.Stop()` 一次也不会
// 执行，Hub.CloseAll 全仓零调用，而 systemctl restart 是例行操作
// （health.go:441 记着「主控每重启一次，所有节点都被自动摘掉」）。
//
// 后果：连接池不回收、隧道不通知节点、WS 客户端只能等 TCP 超时。
//
// **判据是「关停动作跑过了」且「serve 自己回来了」**，不是「进程没崩」——
// 后者在一个直接 os.Exit 的实现下同样为真，而那正是原先的样子。
func TestServeShutsDownGracefully(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	var closed atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, addr, h, func() { closed.Add(1) }) }()

	// 等它真的在听——否则下面取消的是一个还没起来的服务。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("优雅关停不该回错: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消之后 serve 没有返回 —— 那些 defer 一个都不会跑")
	}

	if closed.Load() != 1 {
		t.Errorf("关停动作跑了 %d 次，想要 1 —— "+
			"隧道不通知节点、WS 客户端只能等 TCP 超时", closed.Load())
	}
}

// TestServeReturnsWhenItCannotListen：端口起不来时要**报错返回**，不是挂住。
//
// serve 起了一个 goroutine 等 ctx 结束再关停，而主流程在 ListenAndServe 之后
// 无条件 `<-idle`。bind 阶段就失败时（端口被占、地址非法）ListenAndServe
// 立刻返回，而那个 goroutine 还堵在 `<-ctx.Done()` 上——于是 serve 永远不返回：
// **主控既不退出也不报错，systemd 看到的是一个「活着」的进程**。
//
// 原先那条 `srv.Run` 至少会 os.Exit(1)。这是关停路径改造引入的（issue #65）。
//
// TestServeShutsDownGracefully 特意在 Listen 之后 Close 才用那个地址，
// 恰好绕开了这条路径——**修复只在测试造的那条路上成立**。
func TestServeReturnsWhenItCannotListen(t *testing.T) {
	// 占住一个端口，让 serve 绑不上。
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	done := make(chan error, 1)
	go func() {
		done <- serve(context.Background(), busy.Addr().String(),
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("绑不上端口却回了 nil —— 调用方会以为是正常关停")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("端口被占，serve 没有返回 —— 主控既不退出也不报错，" +
			"而 systemd 看到的是一个活着的进程")
	}
}
