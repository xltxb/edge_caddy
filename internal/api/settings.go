package api

import (
	"errors"
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
		"master_endpoint":         sys.MasterEndpoint,
		"heartbeat_interval_s":    sys.HeartbeatInterval,
		"offline_threshold_count": sys.OfflineThreshold,
		"auto_drop_dns":           sys.AutoDropDNS,
		"warn_cpu_pct":            sys.WarnCPUPct,
		"warn_mem_pct":            sys.WarnMemPct,
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
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, CodeBadParam, "请求格式错误")
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
		// PRD §5 要求主控接入强制域名而非 IP：IP 一旦变更，全部已接入的节点
		// 都要重新接入，而域名换个 A 记录就行。
		if err := validateEndpointIsDomain(*req.MasterEndpoint); err != nil {
			FailValidation(c, "系统设置未通过校验", []FieldError{
				{ResKey: "settings", Field: "master_endpoint", Reason: err.Error()},
			})
			return
		}
		cur.MasterEndpoint = *req.MasterEndpoint
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
