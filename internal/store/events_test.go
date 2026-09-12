package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/testdb"
)

// TestCountReconnectsByNodeAnswersForEveryoneAtOnce：一次查回全体的重连次数。
//
// GET /nodes 原先在节点循环体内逐台调 CountReconnects，6 台就是 6 个往返
// （issue #59）。而那个查询在 events 上没有可用索引，只能 Seq Scan——
// events 又是只写不清的表（#34），行数随时间线性涨，而节点列表页是常驻轮询的。
//
// ctx 一超时，reconnects_1h 会从某一行开始整片变 null，**恰好是这个字段
// 最该说话的时候**。
func TestCountReconnectsByNodeAnswersForEveryoneAtOnce(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()

	for _, id := range []string{"node-a", "node-b", "node-c"} {
		if err := st.UpsertNode(ctx, store.NodeSpec{
			NodeID: id, City: "香港", Vendor: "v", Line: "l", PublicIP: "203.0.113.7",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// a 重连两次、b 一次、c 零次；另外给 a 记一条别的事件，它不该被数进来。
	write := func(node, msg string) {
		if _, err := st.InsertEvent(ctx, node, "warn", msg); err != nil {
			t.Fatal(err)
		}
	}
	write("node-a", store.EventTunnelReconnected)
	write("node-a", store.EventTunnelReconnected)
	write("node-b", store.EventTunnelReconnected)
	write("node-a", store.EventNodeJoined) // 首次加入不是重连——两件事此前记的是同一句话

	got, err := st.CountReconnectsByNode(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got["node-a"] != 2 {
		t.Errorf("node-a 重连 2 次，数出来是 %d（「节点已接入」被数进来了？）", got["node-a"])
	}
	if got["node-b"] != 1 {
		t.Errorf("node-b 重连 1 次，数出来是 %d", got["node-b"])
	}
	// **没重连过的节点不出现在 map 里，而不是出现并等于 0。**
	// 调用方读 map 拿到的零值就是 0，两者在那一侧一样；区别在这里省掉一行。
	if _, ok := got["node-c"]; ok && got["node-c"] != 0 {
		t.Errorf("node-c 没重连过，却数出 %d", got["node-c"])
	}
}

// TestEventsHaveAnIndexForPerNodeQueries：events 上要有一个能服务按节点查询的索引。
//
// 建表时只有 idx_events_created（created_at DESC），而 CountReconnects 的
// WHERE 是 node_id + msg + created_at —— 无索引可用，只能全表扫。
//
// **不用 EXPLAIN 断言**：测试库里只有几行，PG 总会选 Seq Scan，那条断言会
// 永远红。这里查的是「这张表上有没有一个以 node_id 打头的索引」——
// 它是一个结构事实，而结构正是迁移要负责的东西。
func TestEventsHaveAnIndexForPerNodeQueries(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()

	var n int
	err := st.Pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes
		  WHERE tablename = 'events' AND indexdef LIKE '%(node_id%'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("events 上没有以 node_id 打头的索引 —— " +
			"而 GET /nodes 会为每台节点在这张只写不清的表上扫一遍全表")
	}
}
