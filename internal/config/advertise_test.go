package config_test

import (
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/config"
)

// **公布地址必须是域名，不能是 IP（#24）。**
//
// 代价出现在主控换地址那一天：用 IP 的话，每台节点 EnvironmentFile 里的
// --master 都要挨台改，改完之前全部断连；用域名的话改一条 DNS 记录，节点无感。
//
// 贵的是「挨台改」，不是重签证书——内部 PKI 重签一次 SignServer 就行。
// 而服务端证书 TTL 是十年，这笔账要么不付，要么换地址那天一次性全付。
func TestValidateAdvertise(t *testing.T) {
	cases := []struct {
		in     string
		ok     bool
		reason string
	}{
		{"ec.example.com:9000", true, "域名带端口"},
		{"ec.example.com", true, "域名不带端口"},
		{"localhost:9000", true, "localhost 是主机名不是 IP —— 本地开发要能跑"},
		{"192.0.2.1:9000", false, "IPv4 带端口"},
		{"127.0.0.1:9000", false, "回环也是 IP —— 这原先是默认值"},
		{"192.0.2.1", false, "IPv4 不带端口"},
		{"[2001:db8::1]:9000", false, "IPv6 带端口"},
		{"2001:db8::1", false, "IPv6 不带端口（SplitHostPort 会在这里失败，别被它骗过去）"},
		{":9000", false, "只有端口，没有主机名"},
		{"", false, "没设置"},
	}
	for _, c := range cases {
		err := config.ValidateAdvertise(c.in)
		if c.ok && err != nil {
			t.Errorf("%s：%q 应当通过，却报 %v", c.reason, c.in, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s：%q 应当被拒绝", c.reason, c.in)
		}
	}
}

// 错误信息是硬校验之后唯一的救援。
//
// 选了「拒绝启动」就意味着人会在一个起不来的主控面前，只有这一行字可看。
// 它必须说清三件事：为什么拒、改哪个变量、以及**改成什么样才对**。
func TestAdvertiseErrorTellsPeopleWhatToDo(t *testing.T) {
	err := config.ValidateAdvertise("203.0.113.7:9000")
	if err == nil {
		t.Fatal("IP 应当被拒绝")
	}
	msg := err.Error()
	for _, want := range []string{"EC_ADVERTISE", "域名", "203.0.113.7"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息里应当有 %q：%s", want, msg)
		}
	}
	// 把人填错的那个值原样回显 —— 环境变量常常是从别处继承来的，
	// 人未必知道自己「填」过它。
	if !strings.Contains(msg, "203.0.113.7:9000") {
		t.Errorf("应当回显实际读到的值，否则人不知道它从哪儿来：%s", msg)
	}

	// 没设置和设错了是两回事，措辞要分开：前者是「你还没填」，
	// 后者是「你填的这个不行」。混成一句会让人去找一个他根本没写过的地方。
	empty := config.ValidateAdvertise("").Error()
	if empty == msg {
		t.Error("「没设置」与「填了 IP」应当给不同的说法")
	}
}

// **一个翻开之后什么也不做的安全开关，比没有这个开关危险得多。**
//
// ADR-0013 说控制台的 mTLS 以 tls.Config.ClientAuth 实现为一个默认关的开关，
// 而那一半从来没有写。EC_MTLS=1 此前唯一的效果是把会话 Cookie 标成 Secure
// ——那与 mTLS 是两件事，而且会让人在纯 HTTP 的主控上登录不上。
//
// 人会以为控制台开着 mTLS，而它没有；**而这件事不会有任何症状**，
// 直到有人真的去中间人。
func TestMTLSSwitchRefusesToPretend(t *testing.T) {
	if err := config.ValidateMTLS(false); err != nil {
		t.Fatalf("默认关是正常状态，不该报错：%v", err)
	}

	err := config.ValidateMTLS(true)
	if err == nil {
		t.Fatal("EC_MTLS=1 应当拒绝启动 —— 一个静默的假象比一个响亮的失败危险")
	}
	msg := err.Error()
	// 错误信息要说清三件：它没实现、此前那个副作用会造成什么、现在该怎么办。
	for _, want := range []string{"还没有实现", "Cookie", "EC_MTLS=0"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息里应当有 %q：%s", want, msg)
		}
	}
	// 指向 ADR，而不是让人去猜「那一半」指什么。
	if !strings.Contains(msg, "ADR-0013") {
		t.Errorf("应当指向 ADR-0013：%s", msg)
	}
}

// **`wss://` 那种写法也要通过校验，而且要取得出正确的主机名。**
//
// 这条路是为了穿过只转发 80/443 的中间设施：灰度上主控域名挂在 Cloudflare
// 后面，CDN 不代理 9000，节点装完一切正常然后永远不出现在控制台里。
func TestAdvertiseAcceptsTunnelURL(t *testing.T) {
	for _, v := range []string{
		"wss://cdn.example.com",
		"wss://cdn.example.com/api/v1/tunnel",
		"wss://cdn.example.com:8443",
		"ws://localhost:8080", // 本地调试
	} {
		if err := config.ValidateAdvertise(v); err != nil {
			t.Errorf("%q 应当通过校验：%v", v, err)
		}
	}
}

// **主机名取错的后果不是「取错了」，是里层 TLS 报「证书不适用于该主机名」。**
//
// SplitHostPort 对 `wss://cdn.example.com` 会得出荒谬的结果，
// 而它的症状出现在证书上——人会去查证书，那儿没有问题。
func TestAdvertiseHostForSAN(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"ec.example.com:9000", "ec.example.com"},
		{"wss://cdn.example.com", "cdn.example.com"},
		{"wss://cdn.example.com:8443", "cdn.example.com"},
		{"wss://cdn.example.com/api/v1/tunnel", "cdn.example.com"},
		{"localhost:9000", "localhost"},
	} {
		if got := config.AdvertiseHost(c.in); got != c.want {
			t.Errorf("AdvertiseHost(%q) = %q，想要 %q —— "+
				"它进服务端证书的 SAN，错了会表现成「证书不适用于该主机名」",
				c.in, got, c.want)
		}
	}
}

// **URL 形式里填 IP 一样要拒。** 那条限制的理由（换地址那天不用挨台改节点）
// 跟走哪条传输毫无关系，所以两种写法不能有一种漏掉。
func TestAdvertiseRejectsIPInBothForms(t *testing.T) {
	for _, v := range []string{"203.0.113.7:9000", "wss://203.0.113.7", "wss://203.0.113.7:443"} {
		if err := config.ValidateAdvertise(v); err == nil {
			t.Errorf("%q 是 IP，应当被拒", v)
		}
	}
}

// **`https://` 不能被默默当成 `wss://`。**
//
// 悄悄纠正的话，写错的人学不到那个区别，而下一次他会在别的地方
// （比如 nginx 配置）再写错一遍——那时没有人纠正他。
func TestAdvertiseRejectsHTTPScheme(t *testing.T) {
	err := config.ValidateAdvertise("https://cdn.example.com")
	if err == nil {
		t.Fatal("https:// 应当被拒 —— 隧道走 wss://，两者不是一回事")
	}
}
