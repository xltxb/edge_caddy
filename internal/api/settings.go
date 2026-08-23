package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/xltxb/edge_caddy/internal/dnsctl"
	"github.com/xltxb/edge_caddy/internal/dnsops"
	"github.com/xltxb/edge_caddy/internal/store"
)

var (
	errEmptyEndpoint = errors.New("主控地址不能为空")
	// PRD §5：强制域名而非 IP。IP 一旦变更，全部已接入的节点都要重新接入，
	// 而域名换个 A 记录就行。
	errEndpointIsIP = errors.New("请填域名而不是 IP —— IP 变更会导致全部节点需要重新接入")
)

func (s *Server) handleGetSettings(c *gin.Context) {
	sys, err := s.store.GetSystemSettings(c.Request.Context())
	if err != nil {
		s.log.Error("读取系统设置失败", "err", err)
		Fail(c, CodeDownstream, "读取系统设置失败")
		return
	}
	// sealer 传 nil：这个端点只回「配没配」，不解密（PRD §7）。
	dns, err := s.store.GetDNSProvider(c.Request.Context(), nil)
	if err != nil {
		s.log.Error("读取 DNS 服务商设置失败", "err", err)
		Fail(c, CodeDownstream, "读取系统设置失败")
		return
	}

	OK(c, gin.H{
		// **回主控真正在用的那个值，不是库里那一列。**
		//
		// 库里那一列存得下、读得出，而**没有任何东西用它**：拼安装命令用的是
		// EC_ADVERTISE（o.MasterAddr）。人在设置页改它，什么也不会发生
		// ——而那一栏的标签写着「Agent 连接地址」，看起来正是控制这件事的。
		//
		// 「有人读」和「读对了」是两件事，而 scripts/unread.py 只答得出前一件。
		"master_endpoint":          s.masterAddr,
		"master_endpoint_readonly": true,
		"heartbeat_interval_s":     sys.HeartbeatInterval,
		"offline_threshold_count":  sys.OfflineThreshold,
		"auto_drop_dns":            sys.AutoDropDNS,
		"warn_cpu_pct":             sys.WarnCPUPct,
		"warn_mem_pct":             sys.WarnMemPct,
		"dns_provider": gin.H{
			"kind":            dns.Kind,
			"domain":          dns.Domain,
			"sub":             dns.SubName,
			"credential_mode": dns.CredentialMode,
			"configured":      dns.CredentialOK,
		},

		// **每个 kind × mode 还要人填哪些字段。**
		//
		// 由 store.MissingFields 对一份空配置求值得出，所以它不可能和
		// 校验分叉。界面拿它检查「这个 kind+mode 下该有的输入框都渲染出来了吗」
		// ——起因是 account_id 曾被误关在 global_key 分支里，
		// **api_token 模式下那个框根本不存在**，人填不了它。
		//
		// 两条检查合起来才闭合：主控这侧保证「拼进 URL 的值必须被要求填」
		// （TestEveryConfigValueInAURLPathIsRequired），界面那侧保证
		// 「被要求填的必须填得进去」。
		"dns_provider_requirements": store.ProviderRequirements(),

		"ops_bot_token_configured": s.opsBotConfigured,
	})
}

type dnsProviderReq struct {
	Kind           *string `json:"kind"`
	Domain         *string `json:"domain"`
	Sub            *string `json:"sub"`
	AccountID      *string `json:"account_id"`
	ZoneID         *string `json:"zone_id"`
	Email          *string `json:"email"`
	CredentialMode *string `json:"credential_mode"`
	// Credential 空串表示保持不变——凭证不回显，前端也带不出原值（PRD §7）。
	Credential *string `json:"credential"`

	// Clear 把整个服务商配置连同凭证一起清掉。
	//
	// **需要一个独立的动作，而不是「把每个字段填成空串」。** 空串对凭证
	// 是「不改动」（见上），所以逐字段清空之后会留下一个能到达的矛盾状态：
	// 库里有凭证、而没有服务商——一把再也用不到、也删不掉的 API Token。
	Clear *bool `json:"clear"`
}

type systemReq struct {
	DNSProvider      *dnsProviderReq `json:"dns_provider"`
	MasterEndpoint   *string         `json:"master_endpoint"`
	HeartbeatSeconds *int            `json:"heartbeat_interval_s"`
	OfflineThreshold *int            `json:"offline_threshold_count"`
	AutoDropDNS      *bool           `json:"auto_drop_dns"`
}

func (s *Server) handlePutSettings(c *gin.Context) {
	var req systemReq
	// **严格绑定：拒绝契约里没有的字段。**
	//
	// 静默忽略的话，一个写错的 key 会得到 code 0 而什么也没存
	// —— 前端就是这么撞上的（发了顶层 dns_credential 而不是
	// dns_provider.credential，返回成功、configured 一直 false）。
	if err := bindStrict(c, &req); err != nil {
		// **把 bindStrict 的话原样带出去。**
		//
		// 这里原先回一句固定的「请求格式错误」——而 bindStrict 特意点了
		// 那个写错的字段名。一个只说「格式错误」的响应，会让人去检查 JSON
		// 的括号，而问题是他把嵌套的 key 写成了顶层。
		//
		// **做了一个诊断，然后在调用点把它扔掉**——这个仓库里出现过好几次，
		// 而这次是我在写它的同一段工作里犯的。
		Fail(c, CodeBadParam, err.Error())
		return
	}

	ctx := c.Request.Context()
	cur, err := s.store.GetSystemSettings(ctx)
	if err != nil {
		s.log.Error("读取系统设置失败", "err", err)
		Fail(c, CodeDownstream, "保存失败")
		return
	}

	if req.MasterEndpoint != nil {
		// **它在运行时改不了，所以拒绝而不是假装存下。**
		//
		// 这个地址进了**主控服务端证书的 SAN**，而那张证书是启动时签的
		// （tunnel.New 的 Advertise）。改设置改不了证书——那时节点会连上一个
		// 证书里没有它的地址，握手直接失败。
		//
		// 此前这里会把新值存进库，而库里那一列没有任何东西读
		// ——人改完看到「已保存」，节点的连接地址一个字没变。
		FailValidation(c, "系统设置未通过校验", []FieldError{
			{ResKey: "settings", Field: "master_endpoint", Reason: fmt.Sprintf(
				"这个地址由启动配置 EC_ADVERTISE 决定（当前 %q），运行时改不了："+
					"它进了主控服务端证书的 SAN，而证书是启动时签的。"+
					"要改就改环境变量再重启主控。", s.masterAddr)},
		})
		return
	}
	if req.HeartbeatSeconds != nil {
		if *req.HeartbeatSeconds < 1 || *req.HeartbeatSeconds > 60 {
			FailValidation(c, "系统设置未通过校验", []FieldError{
				{ResKey: "settings", Field: "heartbeat_interval_s", Reason: "心跳间隔应在 1-60 秒之间"},
			})
			return
		}
		cur.HeartbeatInterval = *req.HeartbeatSeconds
	}
	if req.OfflineThreshold != nil {
		if *req.OfflineThreshold < 1 || *req.OfflineThreshold > 20 {
			FailValidation(c, "系统设置未通过校验", []FieldError{
				{ResKey: "settings", Field: "offline_threshold_count", Reason: "离线阈值应在 1-20 次之间"},
			})
			return
		}
		cur.OfflineThreshold = *req.OfflineThreshold
	}
	if req.AutoDropDNS != nil {
		cur.AutoDropDNS = *req.AutoDropDNS
	}

	if err := s.store.PutSystemSettings(ctx, cur); err != nil {
		s.log.Error("保存系统设置失败", "err", err)
		Fail(c, CodeDownstream, "保存失败")
		return
	}

	// **detail 必须在与 DNS 无关时保持空串**（契约 §0.4）：
	// 一句「解析已同步」出现在只改了心跳间隔的那次响应里，是在报告一件没发生的事。
	synced, detail := false, ""

	if p := req.DNSProvider; p != nil {
		dns, err := s.store.GetDNSProvider(ctx, nil)
		if err != nil {
			s.log.Error("读取 DNS 服务商设置失败", "err", err)
			Fail(c, CodeDownstream, "保存失败")
			return
		}
		if p.Clear != nil && *p.Clear {
			// **clear 与其他字段同时出现时拒绝，不静默取舍。**
			//
			// `{"clear":true,"kind":"dnspod"}` 有两种合理读法（先清再设 /
			// 清掉一切），而挑一种执行等于替人做了他没做的决定。
			if p.Kind != nil || p.Domain != nil || p.Sub != nil ||
				p.AccountID != nil || p.ZoneID != nil || p.Email != nil ||
				p.CredentialMode != nil || p.Credential != nil {
				FailValidation(c, "系统设置未通过校验", []FieldError{
					{ResKey: "settings", Field: "dns_provider.clear", Reason: "clear 是一个独立的动作，不能与其他字段同时给"},
				})
				return
			}
			if err := s.store.PutDNSProvider(ctx,
				store.DNSProviderSettings{ClearCredential: true}, s.sealer); err != nil {
				s.log.Error("清除 DNS 服务商设置失败", "err", err)
				Fail(c, CodeDownstream, "保存失败")
				return
			}
			OK(c, nil)
			return
		}
		assign(&dns.Kind, p.Kind)
		assign(&dns.Domain, p.Domain)
		assign(&dns.SubName, p.Sub)
		assign(&dns.AccountID, p.AccountID)
		assign(&dns.ZoneID, p.ZoneID)
		assign(&dns.Email, p.Email)
		assign(&dns.CredentialMode, p.CredentialMode)
		assign(&dns.Credential, p.Credential)

		// 名单只有一份（store.ProviderKinds）。此前这里是写死的两个字符串，
		// 而装配那边是另一个 switch —— 加一家时改一处忘一处，
		// 症状是「保存成功、装配时报未知服务商」，或者反过来。
		if dns.Kind != "" && !store.KnownKind(dns.Kind) {
			FailValidation(c, "系统设置未通过校验", []FieldError{
				{ResKey: "settings", Field: "dns_provider.kind",
					Reason: "只能是 " + strings.Join(store.ProviderKinds, " 或 ")},
			})
			return
		}

		// **一份存得下、而用不了的配置，比没配更坏。**
		//
		// 灰度上撞到的：只填了 kind 和凭证、没填域名。保存成功、设置页显示
		// 「已配置」，而 DNS 页说「尚未配置服务商」——**两个端点对同一件事
		// 说了相反的话，而两句在各自的口径下都对**。人看到的是：
		// 填完保存成功、徽标变绿、解析一动不动，没有一处说得出缺了什么。
		//
		// 判据用 store.MissingFields，与装配服务商那一侧**共用同一个函数**：
		// 分开写的话，加一个新的必填字段时改了一侧忘了另一侧，
		// 症状就是这次这个，而它不报错。
		//
		// 「一个字段都没填」是另一回事——那是还没开始配，不是配错了。
		if !dns.Usable() && (dns.Kind != "" || dns.Domain != "" || dns.CredentialOK) {
			var issues []FieldError
			for _, f := range dns.MissingFields() {
				issues = append(issues, FieldError{
					ResKey: "settings", Field: "dns_provider." + f,
					Reason: "配置 DNS 服务商时这一项必填 —— 少了它解析不会被推到任何地方",
				})
			}
			FailValidation(c, "DNS 服务商配置不完整", issues)
			return
		}
		if err := s.store.PutDNSProvider(ctx, dns, s.sealer); err != nil {
			s.log.Error("保存 DNS 服务商设置失败", "err", err)
			Fail(c, CodeDownstream, "保存失败")
			return
		}
		synced, detail = s.syncAfterProviderChange(ctx)
	}
	OK(c, gin.H{"dns_synced": synced, "detail": detail})
}

// syncAfterProviderChange 在服务商设置改动之后**立刻推一次**。
//
// **不推的话，配好服务商是一个什么也不会发生的动作。**
//
// 灰度上撞到的：把服务商从 cloudflare 换成 cloudflare_dns、填好凭证、
// 保存成功、徽标变绿 —— 而 Cloudflare 那边一条记录都没有。
// 因为触发同步的是「保存权重 / 动节点开关 / 改节点 IP / 心跳摘挂」，
// **改服务商本身不在其中**。人得再去随便碰一个别的东西才会推。
//
// 这是这个仓库里数到第六次的同一个形状：**机制建好了，
// 没接到最该接的那个输入上**。而它每次的症状都一样——
// 每一步都成功，而什么也没发生。
//
// 三种结果分开说，因为人接下来的动作不同：
//
//	推上去了       什么都不用做
//	推不上去       看 detail 里服务商回的原话
//	没东西可推      解析轮换是空的 —— 去把节点的解析开回来，不是去查凭证
func (s *Server) syncAfterProviderChange(ctx context.Context) (bool, string) {
	if s.dns == nil {
		return false, ""
	}
	switch err := s.dns.Sync(ctx, nil); {
	case errors.Is(err, dnsops.ErrNoProvider):
		// 配到一半（缺必填项）在上面就被拦下了，走到这里说明是清空了配置。
		return false, ""
	case err != nil:
		var capErr *dnsctl.ErrCapability
		if errors.As(err, &capErr) {
			// **能力不足要与「下游失败」分开。** 后者会让人去查网络、查凭证，
			// 而「没有节点在轮换里」要做的是去把节点的解析开回来。
			return false, "服务商设置已保存，但这次没能推上去：" + capErr.Reason
		}
		s.log.Error("改完服务商后同步解析失败", "err", err)
		return false, "服务商设置已保存，但同步到服务商失败：" + err.Error()
	default:
		// **把实际写入的名字说出来。**
		//
		// domain 填 `cdn.example.com`、sub 又填 `cdn` 的话，记录会建到
		// `cdn.cdn.example.com`——推送成功、这里回 true，而人在服务商面板上
		// 永远看不到它。服务商也不会拦：那是它 zone 里一个合法的子域名。
		//
		// **一次成功里唯一能揭穿这件事的，就是把那个名字印出来。**
		if h := s.dns.Hostname(ctx); h != "" {
			return true, "服务商设置已保存，当前解析已推到服务商（写入 " + h + "）"
		}
		return true, "服务商设置已保存，当前解析已推到服务商"
	}
}

func (s *Server) handleGetAlerts(c *gin.Context) {
	// sealer 传 nil：这个端点只回「配没配」，不解密。
	// 凭证只写入不回显（PRD §7）——两个 webhook 地址本身就是投递权限。
	cfg, err := s.store.GetAlertSettings(c.Request.Context(), nil)
	if err != nil {
		s.log.Error("读取告警设置失败", "err", err)
		Fail(c, CodeDownstream, "读取告警设置失败")
		return
	}
	OK(c, gin.H{
		"notify_level": cfg.NotifyLevel,
		"webhook":      gin.H{"url_configured": cfg.WebhookSet},
		"lark": gin.H{
			"webhook_configured": cfg.LarkSet,
			"at_all_on_crit":     cfg.AtAllOnCrit,
		},
	})
}

type alertsReq struct {
	NotifyLevel *string `json:"notify_level"`
	WebhookURL  *string `json:"webhook_url"`
	LarkWebhook *string `json:"lark_webhook"`
	AtAllOnCrit *bool   `json:"at_all_on_crit"`
}

// handlePutAlerts 保存告警设置。
//
// **请求体是平的，跟 GET 的形状不一样**（契约 §11）。GET 里只有
// `url_configured: true/false`，没有地方放 webhook 地址——凭证只写入不回显——
// 所以 PUT 的字段集必然与 GET 不同。
//
// 契约原先把两个端点并成一个代码块，前端照着发了 GET 的形状：
// `at_all_on_crit` 包在 `lark` 里，而这里它在顶层。ShouldBindJSON 静默丢掉，
// 返回 code 0，界面显示「已保存」——**那个开关从来没存进去过**。
//
// 所以这里跟 PUT /settings 一样用严格绑定：形状发错了当场说出哪个字段不认识。
func (s *Server) handlePutAlerts(c *gin.Context) {
	var req alertsReq
	if err := bindStrict(c, &req); err != nil {
		Fail(c, CodeBadParam, err.Error())
		return
	}
	ctx := c.Request.Context()

	cur, err := s.store.GetAlertSettings(ctx, nil)
	if err != nil {
		s.log.Error("读取告警设置失败", "err", err)
		Fail(c, CodeDownstream, "保存失败")
		return
	}
	if req.NotifyLevel != nil {
		switch *req.NotifyLevel {
		case "all", "warn", "crit":
			cur.NotifyLevel = *req.NotifyLevel
		default:
			FailValidation(c, "告警设置未通过校验", []FieldError{
				{ResKey: "alerts", Field: "notify_level", Reason: "只能是 all / warn / crit"},
			})
			return
		}
	}
	if req.AtAllOnCrit != nil {
		cur.AtAllOnCrit = *req.AtAllOnCrit
	}
	// **空串表示保持不变**：凭证不回显，因此前端提交时也带不出原值来。
	if req.WebhookURL != nil {
		cur.WebhookURL = *req.WebhookURL
	}
	if req.LarkWebhook != nil {
		cur.LarkWebhook = *req.LarkWebhook
	}

	if err := s.store.PutAlertSettings(ctx, cur, s.sealer); err != nil {
		s.log.Error("保存告警设置失败", "err", err)
		Fail(c, CodeDownstream, "保存失败")
		return
	}
	OK(c, nil)
}

type testAlertReq struct {
	Channel string `json:"channel"`
}

func (s *Server) handleTestAlert(c *gin.Context) {
	var req testAlertReq
	if err := bindStrict(c, &req); err != nil {
		Fail(c, CodeBadParam, err.Error())
		return
	}
	setAuditTarget(c, req.Channel)

	if s.alerts == nil {
		Fail(c, CodeStateConflict, "告警未装配")
		return
	}
	if err := s.alerts.Test(c.Request.Context(), req.Channel); err != nil {
		// 下游的原文是排查 webhook 配错的唯一线索，原样带上。
		Fail(c, CodeDownstream, err.Error())
		return
	}
	OK(c, gin.H{"sent": true, "detail": "已投递"})
}

// validateEndpointIsDomain 拒绝 IP 形式的主控地址（PRD §5）。
func validateEndpointIsDomain(endpoint string) error {
	host := endpoint
	if h, _, err := net.SplitHostPort(endpoint); err == nil {
		host = h
	}
	if host == "" {
		return errEmptyEndpoint
	}
	if net.ParseIP(host) != nil {
		return errEndpointIsIP
	}
	return nil
}

func assign(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}
