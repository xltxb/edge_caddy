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
