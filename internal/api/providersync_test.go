package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/xltxb/edge_caddy/internal/dnsctl"
	"github.com/xltxb/edge_caddy/internal/dnsops"
)

// 改完服务商之后那一次同步有四种结果，而**人接下来要做的事各不相同**：
//
//	推上去了        什么都不用做
//	没配服务商      去把服务商配上
//	轮换是空的      去把节点的解析开回来 —— 凭证和网络都没问题
//	推不上去        看服务商回的原话，查凭证 / 查网络
//
// 后两种混在一起的代价是单向的：一句「同步到服务商失败」会把人送去查凭证、
// 查网络、翻服务商的状态页，而那边什么毛病也没有。#36 给「轮换是空的」分了
// 单独的错误类型正是为了这个，dns.go 的 handlePutDNSWeights 也按它分了档
// （issue #81）——而这条路上没有。
func TestProviderSyncTellsEmptyRotationApartFromAFailure(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		wantOK bool
		want   string // detail 里必须出现的那个词
		avoid  string // 不能出现的那个词 —— 它会把人送错方向
	}{
		{"推上去了", nil, true, "", ""},
		{"没配服务商", dnsops.ErrNoProvider, false, "没有可用的 DNS 服务商配置", ""},
		{
			"轮换是空的",
			&dnsctl.ErrNothingInRotation{Reason: "没有任何节点在解析轮换里，本次不改动 DNS 记录"},
			false, "解析轮换里", "失败",
		},
		{
			"这家服务商表达不了",
			&dnsctl.ErrCapability{Reason: "DNSPod 不支持按权重分配"},
			false, "DNSPod 不支持按权重分配", "",
		},
		{"服务商那边出错了", errors.New("502 Bad Gateway"), false, "502 Bad Gateway", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, detail := providerSyncDetail(c.err)
			if ok != c.wantOK {
				t.Fatalf("ok = %v，想要 %v", ok, c.wantOK)
			}
			if c.want != "" && !strings.Contains(detail, c.want) {
				t.Fatalf("detail = %q，里面应当有 %q", detail, c.want)
			}
			if c.avoid != "" && strings.Contains(detail, c.avoid) {
				t.Fatalf("detail = %q，不该出现 %q —— 这会把人送去查凭证和网络，"+
					"而要做的是把节点的解析开回来", detail, c.avoid)
			}
			if !ok && detail == "" {
				t.Fatal("说了没推上去就得说为什么 —— 契约那张表承诺 false 时给出理由")
			}
		})
	}
}
