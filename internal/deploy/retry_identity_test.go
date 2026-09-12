package deploy_test

import (
	"testing"
	"time"
)

// TestCancelAllStopsTheJobThatIsActuallyRunning：CancelAll 要停掉**此刻在跑的
// 那个**补推，而不是被一个已经退出的同名任务把它从册子上划掉。
//
// enqueue 的注释明写它支持同一次下发二次入队（「它会先取消同一次下发上尚未
// 结束的补推」）。而清理时不比对身份：
//
//	if r.running[job.deployID] != nil { delete(r.running, job.deployID) }
//
// 老任务退出时把**新任务**的 cancel 从 map 里删掉，此后 CancelAll() 扫不到它
// （issue #60）。一次迟到的补推就能把旧配置推给已经拿到新版的节点——
// 而那正是 deploy.go 里 CancelAll 存在的全部理由。
func TestCancelAllStopsTheJobThatIsActuallyRunning(t *testing.T) {
	p := newFakePusher("node-a")
	p.plan("node-a", timeout()) // 一直不回应，补推会一轮轮退避下去
	sched, _ := newSched(t, p)

	sched.EnqueueRetry(7, "cfg-1", []string{"node-a"})
	// 同一个 deployID 二次入队：老的被取消，新的接上。
	sched.EnqueueRetry(7, "cfg-1", []string{"node-a"})

	// 等老任务真的退出——它退出时会去动那本册子。
	time.Sleep(80 * time.Millisecond)

	sched.Retries().CancelAll()

	// **先让在途的那一次落地再读数。** 一次已经发出去的推送收不回来，
	// 而在 CancelAll 之后立刻读计数，会把它记成「取消之后又推的」——
	// 那是测量的竞态，不是被测行为（这条是调试时撞出来的）。
	time.Sleep(50 * time.Millisecond)

	// 此后再没有人该推了。给它足够多个退避周期去暴露自己。
	settled := p.attemptsFor("node-a")
	time.Sleep(400 * time.Millisecond)
	if got := p.attemptsFor("node-a"); got != settled {
		t.Errorf("CancelAll 之后又推了 %d 次 —— 那个补推已经不在册子上了，"+
			"而它还在往节点上推旧配置", got-settled)
	}
	sched.Retries().Wait()
}
