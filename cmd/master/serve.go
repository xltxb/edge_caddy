package main

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// shutdownGrace 是关停时给在途请求的时间。
//
// 取 10 秒：单次全网推送 6 节点的反馈上限是 10 秒（PRD），而一次正在进行的
// 下发被掐断，节点侧的状态与库里的记录会对不上。
const shutdownGrace = 10 * time.Second

// serve 起 HTTP 面，并在 ctx 结束时优雅地把它收掉。
//
// # 为什么不用 gin 的 Run
//
// `(*gin.Engine).Run` 是 ListenAndServe，**没有关停钩子**。原先主控就是它，
// 出错直接 os.Exit(1)——于是 `defer st.Close()`、`defer tun.Stop()` 一次也不会
// 执行，Hub.CloseAll 全仓零调用（issue #65）。
//
// 而重启是例行操作：health.go 记着「主控每重启一次，所有节点都被自动摘掉
// （重启窗口里它们必然错过几个心跳）」。代价是连接池不回收、隧道不通知节点、
// WS 客户端只能等 TCP 超时。
//
// # onShutdown
//
// 那些「要主动通知对端」的事——断开 WS、停隧道——在这里做，而不是靠 defer：
// defer 只在函数正常返回时跑，而这个函数原先根本不返回。
func serve(ctx context.Context, addr string, h http.Handler, onShutdown func()) error {
	srv := &http.Server{Addr: addr, Handler: h}

	idle := make(chan struct{})
	go func() {
		defer close(idle)
		<-ctx.Done()

		if onShutdown != nil {
			// **先通知对端，再停止接受新连接。** 反过来的话，那些还连着的
			// 客户端会在一个已经不收新连接的服务上继续等，直到 TCP 超时。
			onShutdown()
		}
		// 关停用一个**独立的** ctx：传进来的那个已经结束了，
		// 拿它去 Shutdown 等于「立刻掐掉」，而那正是要避免的事。
		stopCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(stopCtx)
	}()

	err := srv.ListenAndServe()
	<-idle
	if errors.Is(err, http.ErrServerClosed) {
		// 这是**被要求**关停的正常出口，不是故障。
		return nil
	}
	return err
}
