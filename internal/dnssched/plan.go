// Package dnssched 把「谁该分到多少流量」算清楚，再交给服务商适配去落地。
//
// 分成两层是因为这两件事的难点完全不同：归一化是纯算术，要的是边界正确；
// 落地是各家服务商的能力差异，要的是不撒谎。
package dnssched

import "sort"

// Lines 是固定的五条线路码（api-contract §8）。
var Lines = []Line{
	{Code: "ct", Name: "电信"},
	{Code: "cu", Name: "联通"},
	{Code: "cm", Name: "移动"},
	{Code: "tw", Name: "台湾"},
	{Code: "ov", Name: "境外"},
}

type Line struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func IsLine(code string) bool {
	for _, l := range Lines {
		if l.Code == code {
			return true
		}
	}
	return false
}

// NodeState 是归一化需要知道的关于一个节点的一切。
type NodeState struct {
	ID         string
	IP         string
	DNSEnabled bool
	Status     string // ok | warn | down
	// Drained 是「这台机器已经被人为下线」（ADR-0014）。已下线的节点
	// **不出现在候选里**——给它配权重是没有意义的动作。
	Drained bool
}

// Entry 是某条线路上某个节点的解析安排。
type Entry struct {
	Node string `json:"node"`
	IP   string `json:"-"`
	// Weight 是**配置值**，Share 是**实际占比**。两者分开：
	// 一个被摘除的节点 Weight 仍是配置的那个数（人没改过它），
	// 而 Share 为 0。前端的占比条画 Share，输入框绑 Weight。
	Weight     int     `json:"weight"`
	Share      float64 `json:"share"`
	DNSEnabled bool    `json:"dns_enabled"`
	Status     string  `json:"status"`
	// InRotation 是「这个节点现在在不在解析里」，服务商适配据此决定去留。
	InRotation bool `json:"-"`
}

type LinePlan struct {
	Code    string  `json:"code"`
	Name    string  `json:"name"`
	Entries []Entry `json:"entries"`
}

// Plan 是一份算好的解析安排。
type Plan struct {
	Domain string     `json:"domain"`
	Lines  []LinePlan `json:"lines"`
}

// Weights 是库里配置的权重：line_code → node_id → weight。
type Weights map[string]map[string]int

// Build 把权重与节点状态算成一份 Plan。
//
// 参与解析的条件是 **dns_enabled 且 status != down**：
//   - dns_enabled=false 是人为暂停，或心跳超时自愈摘除的结果。
//   - status=down 的节点即使 dns_enabled 还是 true（比如刚判定离线、
//     摘除那一步失败了），也不该继续分到流量。两个条件缺一不可。
//
// warn 的节点**仍然参与解析**：它是「连着但不健康」，把它摘掉会把负载全压到
// 其余节点上，很可能连锁。要摘由人决定。
func Build(domain string, weights Weights, nodes []NodeState) Plan {
	byID := make(map[string]NodeState, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}

	plan := Plan{Domain: domain}
	for _, line := range Lines {
		lp := LinePlan{Code: line.Code, Name: line.Name}

		// 候选 = 配过权重的 ∪ 还没下线的节点。
		//
		// **只取前者会形成一个闭环**：一个节点只有在 dns_weights 里有行时才
		// 出现在这一页上，而写那张表的唯一入口是 PUT /dns/weights——
		// 界面上只能改「已经在列表里的节点」。于是**新接入的节点永远进不了解析**，
		// 而页面看起来完全正常：五条线路齐全，只是每条都空着。
		//
		// 为什么不从 node.line 推：那是**机房卖的中转线路**（CN2 GIA、CMIN2），
		// 说的是这台机器怎么出网；line_code 是**访问者来自哪个运营商**。
		// 一台 CN2 GIA 的机器同时服务电信、联通、移动的访问者是常态，
		// 两者之间没有函数关系——「哪台机器接哪条线的流量」是一个调度决定，
		// 只能由人来下，产品要做的是**让这个决定做得出来**。
		seen := make(map[string]bool, len(weights[line.Code])+len(nodes))
		ids := make([]string, 0, len(weights[line.Code])+len(nodes))
		for id := range weights[line.Code] {
			seen[id] = true
			ids = append(ids, id)
		}
		for _, n := range nodes {
			// 已下线的节点不进候选。但**它如果配过权重就仍然出现**（上面那个循环
			// 已经收了），因为那份配置是人写下的意图，不该因为一次下线就从页面上消失。
			if !seen[n.ID] && !n.Drained {
				seen[n.ID] = true
				ids = append(ids, n.ID)
			}
		}
		sort.Strings(ids)

		// 先定谁在轮换里，再按**在轮换里的那些**的权重之和归一化。
		var total int
		entries := make([]Entry, 0, len(ids))
		for _, id := range ids {
			n, known := byID[id]
			w := weights[line.Code][id]
			in := known && n.DNSEnabled && n.Status != "down" && w > 0
			if in {
				total += w
			}
			entries = append(entries, Entry{
				Node: id, IP: n.IP, Weight: w,
				DNSEnabled: n.DNSEnabled, Status: n.Status, InRotation: in,
			})
		}

		for i := range entries {
			// total == 0 时全部为 0，不做除法。整条线路的节点全部离线是
			// 一个真实会发生的状态（一次机房故障就够了），除零或 NaN 会让
			// 这个页面在最需要看的时候崩掉。
			if entries[i].InRotation && total > 0 {
				entries[i].Share = round1(float64(entries[i].Weight) / float64(total) * 100)
			}
		}
		lp.Entries = entries
		plan.Lines = append(plan.Lines, lp)
	}
	return plan
}

// Rotation 返回某条线路上真正参与解析的条目。
func (p Plan) Rotation(lineCode string) []Entry {
	for _, l := range p.Lines {
		if l.Code != lineCode {
			continue
		}
		var out []Entry
		for _, e := range l.Entries {
			if e.InRotation {
				out = append(out, e)
			}
		}
		return out
	}
	return nil
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
