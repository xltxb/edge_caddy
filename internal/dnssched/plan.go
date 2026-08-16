// Package dnssched 把「库里的权重 + 节点在线状态」算成要下发给 DNS 服务商的
// 解析计划。
//
// 这一层是纯计算，没有副作用——它的每一条规则都直接决定线上流量怎么分，
// 而算错了的表现是「界面上写 33%，实际收到 49%」，那种不一致查起来极其费时。
package dnssched

import (
	"errors"
	"fmt"
	"sort"

	"github.com/xltxb/edge_caddy/internal/dnsprovider"
)

// Entry 是一个节点在某条线路上的解析配置。
type Entry struct {
	NodeID string
	IP     string
	Line   dnsprovider.Line
	// Weight 是运维配置的相对权重。0 表示**主动**摘掉这台。
	Weight int
	// Online 来自健康巡检。
	Online bool
}

// MinWeight 是归一化后的权重下限。
//
// 占比极小的节点也至少拿 1：算成 0 等于把它摘出解析，而运维给的是一个正数——
// 那是「少给点流量」，不是「不要流量」。
const MinWeight = 1

// Plan 算出要下发的解析计划。
//
// 规则（每一条都直接决定线上流量怎么分）：
//
//   - 权重为 0 的节点不参与解析。这是运维**主动**摘掉一台的方式。
//   - 离线节点自动退出，其余节点在**本条线路内**重新归一化。
//   - 某条线路全部离线时回落到该线路的全部节点：把所有节点都摘掉等于把域名
//     解析成空，比继续解析到一台可能只是心跳抖动的机器更糟——前者是确定的
//     全站不可用，后者只是可能的部分不可用。
//   - 但权重**全为 0** 时不回落：那是有人一个个敲进去的明确意图，
//     救回来等于推翻一个明确的决定。
func Plan(entries []Entry) ([]dnsprovider.Target, error) {
	if len(entries) == 0 {
		return nil, errors.New("没有任何节点，无法生成解析计划（空计划会把域名解析清空）")
	}
	for _, e := range entries {
		if e.IP == "" {
			// 空 IP 会被服务商拒绝，而报错是「记录值非法」——排查时要先想到
			// 是节点信息不全，那一步很难想到
			return nil, fmt.Errorf("节点 %s 没有 IP，不能进解析计划", e.NodeID)
		}
	}

	byLine := map[dnsprovider.Line][]Entry{}
	for _, e := range entries {
		byLine[e.Line] = append(byLine[e.Line], e)
	}

	var out []dnsprovider.Target
	for _, line := range sortedLines(byLine) {
		targets, err := planLine(line, byLine[line])
		if err != nil {
			return nil, err
		}
		out = append(out, targets...)
	}
	if len(out) == 0 {
		return nil, errors.New("所有节点的权重都是 0，下发下去会把解析清空")
	}
	return out, nil
}

func planLine(line dnsprovider.Line, entries []Entry) ([]dnsprovider.Target, error) {
	// 权重 0 的先剔掉：那是主动摘除，不该被回落救回来
	var eligible []Entry
	for _, e := range entries {
		if e.Weight > 0 {
			eligible = append(eligible, e)
		}
	}
	if len(eligible) == 0 {
		// 这条线路被运维整条清空了。不是错误——别的线路可能还有节点。
		return nil, nil
	}

	var live []Entry
	for _, e := range eligible {
		if e.Online {
			live = append(live, e)
		}
	}
	if len(live) == 0 {
		// 整条线路全掉了：回落到该线路的全部节点，而不是把这条线路解析清空
		live = eligible
	}

	return normalize(live), nil
}

// normalize 把相对权重换算成合计 100 的整数权重。
//
// 舍入用「最大余数法」：各自取整后把差额补给余数最大的几个。简单取整会让
// 三台等权重时合计只有 99——数值不大，但「界面上写 33.3% 实际 33.0%」
// 这种对不上的东西查起来极其费时。
func normalize(entries []Entry) []dnsprovider.Target {
	total := 0
	for _, e := range entries {
		total += e.Weight
	}

	type share struct {
		entry Entry
		base  int
		rem   float64
	}
	shares := make([]share, 0, len(entries))
	assigned := 0
	for _, e := range entries {
		exact := float64(e.Weight) * 100 / float64(total)
		base := int(exact)
		if base < MinWeight {
			base = MinWeight
		}
		assigned += base
		shares = append(shares, share{entry: e, base: base, rem: exact - float64(int(exact))})
	}

	// 按余数从大到小补差额；余数相同时按节点 ID，保证结果稳定——
	// 不稳定的话，同样的输入会产出不同的计划，每次保存都在改线上记录
	sort.Slice(shares, func(i, j int) bool {
		if shares[i].rem != shares[j].rem {
			return shares[i].rem > shares[j].rem
		}
		return shares[i].entry.NodeID < shares[j].entry.NodeID
	})
	diff := 100 - assigned
	for i := 0; diff > 0 && i < len(shares); i++ {
		shares[i].base++
		diff--
	}
	// 因为有下限 MinWeight，assigned 可能超过 100，此时从最大的往下扣，
	// 但不扣到下限以下
	for i := len(shares) - 1; diff < 0 && i >= 0; i-- {
		if shares[i].base > MinWeight {
			shares[i].base--
			diff++
			i = len(shares) // 从头再来一轮，避免把某一个扣穿
		}
	}

	out := make([]dnsprovider.Target, 0, len(shares))
	for _, s := range shares {
		out = append(out, dnsprovider.Target{
			NodeID: s.entry.NodeID, IP: s.entry.IP,
			Line: s.entry.Line, Weight: s.base,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func sortedLines(m map[dnsprovider.Line][]Entry) []dnsprovider.Line {
	out := make([]dnsprovider.Line, 0, len(m))
	for l := range m {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
