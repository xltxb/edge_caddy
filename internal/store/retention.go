package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// 三张只写不清的表各自的保留期。
//
// **它们不共用一个数，因为它们回答的问题不同：**
//
//   - events 是运维时间线：「这两天发生了什么」。90 天足够覆盖一次季度复盘，
//     而它是三张表里长得最快的（每次心跳状态变化、每次同步都可能写）。
//   - audit_logs 是「谁改的」。ADR-0013 把它列为控制台准入模型的三分之一，
//     而追责这件事的时间尺度以年计——一年。
//
// sessions 不在这里：它删的是**已经过期的**，与保留期无关（见 Prune）。
const (
	eventRetention = 90 * 24 * time.Hour
	auditRetention = 365 * 24 * time.Hour
)

// Prune 清掉三张只写不清的表里已经没有价值的行。
//
// 这三张表原先没有任何删除路径，行数随时间线性涨（issue #34）。
// 它不只是「磁盘会满」：
//
//   - events 上每台节点一次查询（#59）随行数一起变慢
//   - 审计页的第一页被日常写操作填满，失败登录被挤出去（#49）
//   - sessions 里堆着早就过期的行，而每次请求都要查它
//
// **一次跑完三张，任何一张失败都继续跑完剩下的**：它们之间没有依赖，
// 而让一张表的问题挡住另外两张的清理，只会让那两张一起长。
func (s *Store) Prune(ctx context.Context) error {
	var firstErr error
	note := func(what string, err error) {
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("清理 %s: %w", what, err)
		}
	}

	// sessions 删的是**已经过期的**，不是「够老的」。
	// 一条过期会话没有任何价值，多留一天也不会有人去看它；
	// 而一条还没过期的会话被删掉，人会在使用中途被踢出去。
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	note("过期会话", err)

	_, err = s.Pool.Exec(ctx,
		`DELETE FROM events WHERE created_at < now() - $1::interval`,
		eventRetention.String())
	note("事件", err)

	_, err = s.Pool.Exec(ctx,
		`DELETE FROM audit_logs WHERE created_at < now() - $1::interval`,
		auditRetention.String())
	note("审计", err)

	return firstErr
}

// RunPrune 定期清理，直到 ctx 结束。
//
// **启动后先等一个周期再跑第一次**：主控刚起来时正忙着接节点、推配置，
// 而这件事不急于那几分钟。
func (s *Store) RunPrune(ctx context.Context, every time.Duration, log *slog.Logger) {
	if every <= 0 {
		every = time.Hour
	}
	if log == nil {
		log = slog.Default()
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Prune(ctx); err != nil {
				// 记日志不中止循环：一次失败不该让此后再也不清理。
				log.Error("清理历史数据失败", "err", err)
			}
		}
	}
}
