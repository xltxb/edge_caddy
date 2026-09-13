package agent

import "testing"

// TestVerifyAddrAcceptsEquivalentSpellings：指向同一个端点的几种写法要互相认。
//
// normalizeAddr 的注释承诺「让 unix/ 前缀与 host:port 两种写法可比」，
// 而它的实现是 `strings.TrimSpace(a)`——一件都没做（issue #63）。
//
// 于是 EC_VERIFY_LISTEN 写成 `:2020` 或 `0.0.0.0:2020`（与主控渲染的
// `127.0.0.1:2020` 实际等价、连得通），CheckVerifyAddr 照样**整份拒绝下发**，
// 并给出一条「请让两边一致」的错误——而人已经认为它们一致了。
//
// 这条检查本身是对的（两处知识必须对齐，不对齐时每个受保护域名都会 502），
// 问题只在它把「写法不同」当成了「地址不同」。
func TestVerifyAddrAcceptsEquivalentSpellings(t *testing.T) {
	cfg := func(dial string) []byte {
		// 形状要与渲染器产出的一致：**重写到 /verify/ 的 reverse_proxy**。
		// 普通回源也是 reverse_proxy，forwardAuthDials 靠这个 rewrite 区分。
		return []byte(`{"apps":{"http":{"servers":{"edge":{"routes":[{"handle":[
			{"handler":"reverse_proxy","rewrite":{"uri":"/verify/svc-1"},
			 "upstreams":[{"dial":"` + dial + `"}]}]}]}}}}}`)
	}

	same := []struct{ dial, listen string }{
		{"127.0.0.1:2020", ":2020"},
		{"127.0.0.1:2020", "0.0.0.0:2020"},
		{"0.0.0.0:2020", "127.0.0.1:2020"},
		{"localhost:2020", "127.0.0.1:2020"},
		{"unix//run/verify.sock", "unix//run/verify.sock"},
	}
	for _, c := range same {
		if err := CheckVerifyAddr(cfg(c.dial), c.listen); err != nil {
			t.Errorf("dial=%q listen=%q 指向同一个端点，却被拒了：%v", c.dial, c.listen, err)
		}
	}

	// 真不一致的仍然要拒 —— 这条检查存在的理由没有变。
	differ := []struct{ dial, listen string }{
		{"127.0.0.1:2020", "127.0.0.1:2021"},        // 端口不同
		{"127.0.0.1:2020", "unix//run/verify.sock"}, // 一个 tcp 一个 unix
		{"unix//run/a.sock", "unix//run/b.sock"},    // 两个 socket 不是同一个
		{"192.168.1.9:2020", "127.0.0.1:2020"},      // 别的机器
	}
	for _, c := range differ {
		if err := CheckVerifyAddr(cfg(c.dial), c.listen); err == nil {
			t.Errorf("dial=%q listen=%q 不是同一个端点，应当拒绝", c.dial, c.listen)
		}
	}
}
