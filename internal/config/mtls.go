package config

import "fmt"

// ValidateMTLS 拒绝一个还没实现的开关。
//
// ADR-0013 说控制台的 mTLS 以 `tls.Config.ClientAuth` 实现为一个默认关的
// 开关，而**那一半从来没有写**。`EC_MTLS=1` 此前唯一的效果是把会话 Cookie
// 标成 Secure——那与 mTLS 是两件事，而且在纯 HTTP 的主控上会让人**登录不上**
// （浏览器不在 HTTP 连接上发送 Secure Cookie）。
//
// **一个翻开之后什么也不做的安全开关，比没有这个开关危险得多。**
// 人会以为控制台开着 mTLS，而它没有——**而这件事不会有任何症状**，
// 直到有人真的去中间人。这跟一个「开着却没有效果」的限流开关是同一族，
// 只是后果更重。
//
// 拒绝启动是把静默的假象换成响亮的失败。与 EC_ADVERTISE 同一条处置。
func ValidateMTLS(enabled bool) error {
	if !enabled {
		return nil
	}
	return fmt.Errorf("EC_MTLS 打开了，而控制台的 mTLS 还没有实现：\n" +
		"  ADR-0013 说它以 tls.Config.ClientAuth 实现，那一半是空的。\n" +
		"  这个开关此前唯一的效果是把会话 Cookie 标成 Secure，\n" +
		"  而那会让你在纯 HTTP 的主控上登录不上——一个跟 mTLS 无关的故障。\n" +
		"  \n" +
		"  控制台当前的准入是「只绑内网或回环 + Cookie 会话 + 全写审计」\n" +
		"  （ADR-0013）。要从内网挪出去，先实现那一半，\n" +
		"  连同 ADR 里写明的回环逃生口。\n" +
		"  \n" +
		"  现在请设 EC_MTLS=0 或者不设它。")
}
