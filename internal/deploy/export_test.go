package deploy

// SetCommitFault 让测试能确定性地制造「合入基线失败」。
//
// 与 fakePusher 同一条理由（见 retry_test.go 开头）：库是真的，
// 而在真库上确定性地让 commit 这一步单独失败做不到 ——
// 毒数据路线会先被渲染校验拦下。
func (s *Scheduler) SetCommitFault(err error) { s.commitFault = err }

// EnqueueRetry 让包外测试能直接驱动补推，不必先跑一次完整下发。
//
// #60 要验的是「同一次下发二次入队之后，CancelAll 停的是哪一个」——
// 而走 Deploy 那条路每次都会先 CancelAll、再入队一次，制造不出那个状态。
func (s *Scheduler) EnqueueRetry(deployID int64, cfgVersion string, nodes []string) {
	s.Retries().enqueue(retryJob{
		deployID: deployID, cfgVersion: cfgVersion,
		caddyJSON: []byte(`{}`), nodes: nodes,
	})
}
