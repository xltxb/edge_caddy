package store_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xltxb/edge_caddy/internal/model"
	"github.com/xltxb/edge_caddy/internal/testdb"
)

// TestInsertRuleIfAbsentIsAtomic：「只建，不覆盖」不能是 check-then-act。
//
// 契约 §6.2 承诺：带 `If-None-Match: *` 的 PUT 撞上已有 id，回 1004，
// **且一个字节都不写**。而 handler 原先是「先 GetRule 查一遍，没有就
// UpsertRule」——两句之间有窗口，两个人同时新建同一个 id 时两句 GetRule 都
// 说没有，然后两次 Upsert 都写，后写的把先写的整个换掉还回 code: 0。
//
// 那正是 #70 要挡的那件事本身：**静默覆盖别人配好的规则，两边都没有提示**。
// 前端那份重名保护挡不住它，而后端这道闸在并发下也挡不住。
//
// 判据是「几次写成功」，不是「有没有报错」：check-then-act 的实现在这条测试
// 里同样不报错，它只是多写了几次。
func TestInsertRuleIfAbsentIsAtomic(t *testing.T) {
	st := testdb.New(t)
	ctx := context.Background()

	const id = "svc-1"
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ok, err := st.InsertRuleIfAbsent(ctx, model.Rule{
				ID: id, Name: nameOf(n), Type: "ip_whitelist", Enabled: true,
			}, "", nil)
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				created.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if got := created.Load(); got != 1 {
		t.Fatalf("20 个人同时新建同一个 id，%d 次报告「建成了」—— "+
			"契约说撞上已有 id 时一个字节都不写，而这里后写的把先写的换掉了", got)
	}

	// 正向的一半：那唯一一次真的落库了。只数「成功次数」的话，
	// 一个恒回 false 的实现也能让上面那条过。
	r, err := st.GetRule(ctx, id)
	if err != nil {
		t.Fatalf("说建成了，却读不回来：%v", err)
	}
	if r.Name == "" {
		t.Fatal("落库的那条没有名字 —— 写进去的不是提交的那一份")
	}
}

func nameOf(n int) string { return "规则-" + string(rune('a'+n%26)) }
