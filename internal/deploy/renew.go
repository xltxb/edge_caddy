package deploy

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xltxb/edge_caddy/internal/render"
	"github.com/xltxb/edge_caddy/internal/tunnel"
)

// UpstreamRenewalInterval 是回源证书的续期周期。
//
// 叶子 24 小时（ADR-0009），这里取 8 小时：一次推失败还剩两次机会，
// 而节点要连续错过三趟才会真的过期。
const UpstreamRenewalInterval = 8 * time.Hour

// RunUpstreamRenewal 按周期把当前基线重推给每个在线节点，顺带换上新签的回源证书。
//
// # 为什么是重推而不是一条「续期」指令
//
// proto 里 MasterMsg 的 4 号字段就是被删掉的 RenewCert，那条注释写得很清楚：
// 单独的续期指令意味着节点上有一条不经过下发的证书更新路径，而 ADR-0010
// 拒绝 load_files 正是为了不让节点持有那种知识。所以续期只能走下发这一条路。
//
// # 为什么需要这条循环
//
// 续期原先只挂在下发路径上，理由写在 upstreamCertFor 头上：「下发的频率远高于
// 24 小时」。那是一句关于人类操作频率的假设，不是机制。ADR-0009 说的失效条件是
// **失联**超过 24 小时，而真实条件是「没人点下发」超过 24 小时——一个配置稳定、
// 隧道健康、指标全绿的集群会在最后一次下发满 24 小时后回源全断（issue #39）。
//
// # 第一趟为什么不等满一个周期
//
// 主控重启会把周期清零，而重启是例行操作（health.go 记着「主控每重启一次，
// 所有节点都被自动摘掉」）。只按周期跑的话，一个重启比周期还勤的主控永远轮不到
// 续期——那正是这个 bug 换了个触发条件。而 NodeUp 接不住：它 `if !fresh` 就返回，
// 重连回来的老节点不会被推。
//
// 等 every/4 而不是立刻，是因为主控刚起来时 OnlineNodes() 还是空的，
// 节点得先连回来。
func (s *Scheduler) RunUpstreamRenewal(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = UpstreamRenewalInterval
	}
	wait := every / 4
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		s.renewUpstreamCerts(ctx)
		wait = every
	}
}

// renewUpstreamCerts 跑一趟续期。
//
// **成功时什么都不记。** 这一趟不是任何人点出来的，在事件流和下发记录里留行
// 会让「已重推基线」这句话失去它原本的意思——那是一个人点出来的动作的措辞。
// 失败要记：一次推不下去意味着这台机器正在走向回源失效，而那是没人会主动去看的。
func (s *Scheduler) renewUpstreamCerts(ctx context.Context) {
	if s.UpstreamCA == nil || s.Render.UpstreamClientCert == "" {
		// 没配回源 mTLS，就没有要续的东西。这里必须真的空转：
		// 照样推全网等于每 8 小时给每台节点安排一次没有理由的热重载。
		return
	}
	nodes := s.Pusher.OnlineNodes()
	if len(nodes) == 0 {
		return
	}

	log := s.logger()
	baseline, err := s.Store.Baseline(ctx)
	if err != nil {
		log.Error("续期回源证书时读取基线失败", "err", err)
		return
	}
	if baseline == "" {
		// 从没成功下发过：节点上没有回源证书，也就没有要续的。
		return
	}

	// 渲染一次，推给所有人——这一趟给每台机器的配置本来就是同一份基线。
	routes, rules, pol, _, err := s.effective(ctx, nil)
	if err != nil {
		log.Error("续期回源证书时取有效配置失败", "err", err)
		return
	}
	certs, err := s.certsForRender(ctx)
	if err != nil {
		log.Error("续期回源证书时取证书失败", "err", err)
		return
	}
	cfg, issues := render.Render(routes, rules, certs, pol, s.Render)
	if len(issues) > 0 {
		// 基线渲染不过是一件要人去修的事，而它已经会在下发与重推那两条路上说话。
		// 这里只记日志，不再往事件流里灌同一件事。
		log.Error("续期回源证书时基线渲染不过", "issues", issues)
		return
	}
	verifyRules, err := json.Marshal(render.VerifyRules(rules))
	if err != nil {
		log.Error("续期回源证书时序列化校验规则失败", "err", err)
		return
	}
	counts := tunnel.ResourceCounts{
		Routes: uint32(len(routes)),
		Rules:  uint32(countEffectiveRules(rules)),
	}

	for _, nodeID := range nodes {
		out := s.Pusher.Push(ctx, nodeID, baseline, cfg, verifyRules, counts,
			s.upstreamCertFor(nodeID), PushDeadline)
		if !out.OK {
			s.event(ctx, nodeID, "warn", "回源证书续期失败："+out.Detail+
				" —— 这张证书 24 小时后到期，到期后这台机器回源会被源站拒绝")
		}
	}
}
