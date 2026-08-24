package deploy

// SetCommitFault 让测试能确定性地制造「合入基线失败」。
//
// 与 fakePusher 同一条理由（见 retry_test.go 开头）：库是真的，
// 而在真库上确定性地让 commit 这一步单独失败做不到 ——
// 毒数据路线会先被渲染校验拦下。
func (s *Scheduler) SetCommitFault(err error) { s.commitFault = err }
