package alert

import "testing"

// TestDeliveryResultDoesNotComeFromWording：成败是布尔，不是从文案里搜回来的。
//
// `Notify` 里两个 goroutine 各自知道自己成没成，而那个布尔被拼进一句中文
// （`"webhook 失败：" + err`），随后审计那一步再用 `containsFail` 从字符串里
// 搜「失败」二字反推回来（issue #77）。
//
// 今天成立，是因为成功分支的文案恰好不含那两个字——**一句带保质期的推理**。
// 改一次文案它就静默失效，而失效的样子是审计里全绿。
//
// 判据是「拿一条不含『失败』二字的失败结果，审计仍然记 fail」——
// 那正是文案改动会制造出来的情形。
func TestDeliveryResultDoesNotComeFromWording(t *testing.T) {
	cases := []struct {
		name    string
		results []delivery
		want    string
	}{
		{"两条都成", []delivery{{ok: true, detail: "webhook 已投递"}, {ok: true, detail: "lark 已投递"}}, "ok"},
		{"一条没成", []delivery{{ok: true, detail: "webhook 已投递"}, {ok: false, detail: "lark 失败：超时"}}, "fail"},
		{
			// **文案里没有「失败」二字**，而它确实没成。
			// 旧实现在这里会记成 ok，而没有任何东西会说出来。
			name:    "没成，但文案换了说法",
			results: []delivery{{ok: false, detail: "lark 未能送达：群机器人已被移出"}},
			want:    "fail",
		},
		{
			// 反方向：文案里带着「失败」二字，而这一条其实是成功的
			//（比如正文里引用了一条失败告警的标题）。
			name:    "成了，但文案里带着「失败」二字",
			results: []delivery{{ok: true, detail: `webhook 已投递：「节点下线失败」`}},
			want:    "ok",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := overallResult(c.results); got != c.want {
				t.Errorf("记成了 %q，想要 %q —— 成败是每条投递自己知道的事，"+
					"不该从拼好的文案里搜回来", got, c.want)
			}
		})
	}
}
