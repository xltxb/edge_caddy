package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xltxb/edge_caddy/internal/store"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

// 重试只针对**传输层失败**（ADR-0005）。
//
// 节点回应了但 Caddy 拒绝配置的，不重试——同一份字节喂给同一个 Caddy 必然得到
// 同样的拒绝，能修它的是人改配置，不是时间。重试它只会在日志里刷 5 遍一模一样
// 的报错，并且拖着「结束了没有」这个判断不放。
const (
	maxAttempts = 5
	maxBackoff  = 30 * time.Second
)

// defaultBaseBackoff 是第一次重试前的等待，此后翻倍。
const defaultBaseBackoff = time.Second

// Retrier 在后台把掉队的节点补上。
//
// 为什么是后台：5 次指数退避最长要一分多钟，而 PRD 要求单次全网推送
// 6 节点 ≤10s 完成反馈。首轮同步跑完就返回，掉队的交给这里，
// 进度继续经 WS 的 deploy_progress 帧推给前端。
type Retrier struct {
	sched *Scheduler

	mu      sync.Mutex
	running map[int64]*retryHandle // deploy_id → 此刻在跑的那一个
	seq     atomic.Uint64          // 给每次入队一个不重复的身份
	wg      sync.WaitGroup
}

func newRetrier(s *Scheduler) *Retrier {
	return &Retrier{sched: s, running: map[int64]*retryHandle{}}
}

type retryJob struct {
	deployID    int64
	cfgVersion  string
	caddyJSON   []byte
	verifyRules []byte
	counts      tunnel.ResourceCounts
	nodes       []string
}

// enqueue 启动一次后台补推。它会先取消同一次下发上尚未结束的补推。
func (r *Retrier) enqueue(job retryJob) {
	if len(job.nodes) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())

	// **册子上记的是「哪一个」，不只是「有没有」。**
	//
	// 清理时原先只判 `r.running[deployID] != nil` 就删——同一次下发二次入队
	// （enqueue 自己声明支持的场景）时，老任务退出会把**新任务**的 cancel
	// 划掉，此后 CancelAll() 扫不到它（issue #60）。一次迟到的补推就能把
	// 旧配置推给已经拿到新版的节点，而那正是 CancelAll 存在的全部理由。
	//
	// 身份用一个单调递增的序号。
	//
	// **不能用 new(struct{})**：零尺寸类型的指针，Go 规范允许不同分配返回
	// 同一个地址——那样两个任务的「身份」会相等，老任务照样划掉新任务的那一条，
	// 而这个 bug 与它要修的那个长得一模一样（这条是测试抓出来的）。
	me := r.seq.Add(1)

	r.mu.Lock()
	if old := r.running[job.deployID]; old != nil {
		old.cancel()
	}
	r.running[job.deployID] = &retryHandle{id: me, cancel: cancel}
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			// 只划掉自己那一条。册子上已经换成别人了就别动它。
			if cur := r.running[job.deployID]; cur != nil && cur.id == me {
				delete(r.running, job.deployID)
			}
			r.mu.Unlock()
			cancel()
		}()
		r.run(ctx, job)
	}()
}

// CancelAll 停掉全部在飞的补推。新的下发开始时调用。
func (r *Retrier) CancelAll() {
	r.mu.Lock()
	for id, h := range r.running {
		h.cancel()
		delete(r.running, id)
	}
	r.mu.Unlock()
}

// retryHandle 是册子上的一条：谁，以及怎么叫停它。
type retryHandle struct {
	id     uint64
	cancel context.CancelFunc
}

// Wait 等待全部补推结束。测试与优雅关停用。
func (r *Retrier) Wait() { r.wg.Wait() }

func (r *Retrier) run(ctx context.Context, job retryJob) {
	log := r.sched.logger()
	pending := append([]string(nil), job.nodes...)

	for attempt := 1; attempt <= maxAttempts && len(pending) > 0; attempt++ {
		backoff := r.sched.baseBackoff() << (attempt - 1)
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		select {
		case <-ctx.Done():
			r.abandon(job, pending, "已被新的下发取代")
			return
		case <-time.After(backoff):
		}

		// **每次重试前确认这一版仍然是基线。** 不确认的话，一次迟到的重试会把
		// 旧配置盖到一台已经拿到新版本的节点上——那是把节点推回过去，
		// 而现象是「某台机器莫名其妙跑着旧配置」，且下发记录里一切正常。
		if cur, err := r.sched.Store.Baseline(ctx); err == nil && cur != "" && cur != job.cfgVersion {
			r.abandon(job, pending, "已被新的下发取代")
			return
		}

		var still []string
		for _, node := range pending {
			// **每台机器之前都看一眼。** 被取消之后把整批推完，等于在
			// CancelAll 已经说了「停」之后还往剩下的节点上推旧配置。
			if ctx.Err() != nil {
				r.abandon(job, pending, "已被新的下发取代")
				return
			}
			r.sched.progress(job.deployID, job.cfgVersion, node, "run", "", true)
			out := r.sched.Pusher.Push(ctx, node, job.cfgVersion, job.caddyJSON, job.verifyRules,
				job.counts, r.sched.upstreamCertFor(node), PushDeadline)

			switch {
			case out.OK:
				r.record(job, node, "ok", out.Detail, false)
				if err := r.sched.Store.SetNodeCfgVersion(ctx, node, job.cfgVersion); err != nil {
					log.Error("更新节点配置版本失败", "node", node, "err", err)
				}
			case out.Responded:
				// 节点回应了但 Caddy 拒绝——这不是传输层失败，到此为止。
				r.record(job, node, "fail", out.Detail, false)
			default:
				last := attempt == maxAttempts
				detail := out.Detail
				if last {
					detail = fmt.Sprintf("%s（已重试 %d 次）", out.Detail, maxAttempts)
				}
				r.record(job, node, "fail", detail, !last)
				if !last {
					still = append(still, node)
				}
			}
		}
		pending = still
	}
}

// abandon 把还没补上的节点标成终态，附上原因。
//
// 不留 retrying=true：那会让 phase 永远停在 running，而它等的那次重试
// 已经不会发生了。
func (r *Retrier) abandon(job retryJob, nodes []string, reason string) {
	for _, node := range nodes {
		r.record(job, node, "fail", reason, false)
	}
}

func (r *Retrier) record(job retryJob, node, state, detail string, retrying bool) {
	// 用不带取消的 ctx：这一步是把已经发生的事实写下来，
	// 被取消时更需要写——否则那一行会永远停在「重试中」。
	ctx := context.WithoutCancel(context.Background())
	if err := r.sched.Store.SaveDeployResult(ctx, job.deployID, store.DeployResult{
		Node: node, State: state, Detail: detail, Retrying: retrying,
	}); err != nil {
		r.sched.logger().Error("保存重试结果失败", "node", node, "err", err)
	}
	if _, _, err := r.sched.Store.RecountDeploy(ctx, job.deployID); err != nil {
		r.sched.logger().Error("重算下发计数失败", "err", err)
	}
	r.sched.progress(job.deployID, job.cfgVersion, node, state, detail, retrying)
}

func (s *Scheduler) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
