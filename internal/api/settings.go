package api

import (
	"errors"
	"fmt"
	"net"

	"github.com/gin-gonic/gin"
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

	if p := req.DNSProvider; p != nil {
		dns, err := s.store.GetDNSProvider(ctx, nil)
		if err != nil {
			s.log.Error("读取 DNS 服务商设置失败", "err", err)
			Fail(c, CodeDownstream, "保存失败")
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

		if dns.Kind != "" && dns.Kind != "dnspod" && dns.Kind != "cloudflare" {
			FailValidation(c, "系统设置未通过校验", []FieldError{
				{ResKey: "settings", Field: "dns_provider.kind", Reason: "只能是 dnspod 或 cloudflare"},
			})
			return
		}
		if err := s.store.PutDNSProvider(ctx, dns, s.sealer); err != nil {
			s.log.Error("保存 DNS 服务商设置失败", "err", err)
			Fail(c, CodeDownstream, "保存失败")
			return
		}
	}
	OK(c, nil)
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

func (s *Server) handlePutAlerts(c *gin.Context) {
	var req alertsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, CodeBadParam, "请求格式错误")
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
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, CodeBadParam, "请求格式错误")
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
