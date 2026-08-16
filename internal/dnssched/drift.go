package dnssched

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xltxb/edge_caddy/internal/dnsprovider"
)

// WeightDiff 是一条记录在库里与线上的权重差异。
type WeightDiff struct {
	NodeID string           `json:"node"`
	IP     string           `json:"ip"`
	Line   dnsprovider.Line `json:"line"`
	Want   int              `json:"want"`
	Live   int              `json:"live"`
}

// Drift 是「库里的权重」与「线上实际解析」的差异。
//
// 界面必须能看出这个差异：改了权重却没生效时，不看线上就无从察觉——
// 而「以为改了其实没改」是这类系统里最容易出的那种事故。
type Drift struct {
	WeightChanged []WeightDiff          `json:"weight_changed"`
	OnlyPlanned   []dnsprovider.Target  `json:"only_planned"`
	OnlyLive      []dnsprovider.ARecord `json:"only_live"`
}

func (d Drift) Drifted() bool {
	return len(d.WeightChanged) > 0 || len(d.OnlyPlanned) > 0 || len(d.OnlyLive) > 0
}

// Summary 是可直接显示的一句话。没有漂移时为空串。
func (d Drift) Summary() string {
	if !d.Drifted() {
		return ""
	}
	var parts []string
	for _, w := range d.WeightChanged {
		parts = append(parts, fmt.Sprintf("%s（%s）权重库里 %d、线上 %d", w.IP, w.Line, w.Want, w.Live))
	}
	for _, t := range d.OnlyPlanned {
		parts = append(parts, fmt.Sprintf("%s（%s）尚未生效", t.IP, t.Line))
	}
	for _, r := range d.OnlyLive {
		// 线上多出来的最值得说清楚：可能是别人在服务商控制台手动加的，
		// 而它正在分走流量
		parts = append(parts, fmt.Sprintf("%s（%s）线上存在但不在计划内", r.Value, r.Line))
	}
	return strings.Join(parts, "；")
}

type recKey struct {
	line dnsprovider.Line
	ip   string
}

// Diff 比对解析计划与线上实际记录。
//
// 按「线路 + IP」配对：同一个 IP 在不同线路上是**不同的记录**。混为一谈的话，
// 「电信 60 / 境外 40」会被看成同一条 IP 的两个权重，判成漂移或判成一致都不对。
func Diff(planned []dnsprovider.Target, live []dnsprovider.ARecord) Drift {
	liveByKey := map[recKey]dnsprovider.ARecord{}
	for _, r := range live {
		liveByKey[recKey{r.Line, r.Value}] = r
	}
	plannedByKey := map[recKey]dnsprovider.Target{}
	for _, t := range planned {
		plannedByKey[recKey{t.Line, t.IP}] = t
	}

	var d Drift
	for _, t := range planned {
		k := recKey{t.Line, t.IP}
		r, exists := liveByKey[k]
		if !exists {
			d.OnlyPlanned = append(d.OnlyPlanned, t)
			continue
		}
		if r.Weight != t.Weight {
			d.WeightChanged = append(d.WeightChanged, WeightDiff{
				NodeID: t.NodeID, IP: t.IP, Line: t.Line, Want: t.Weight, Live: r.Weight,
			})
		}
	}
	for _, r := range live {
		if _, exists := plannedByKey[recKey{r.Line, r.Value}]; !exists {
			d.OnlyLive = append(d.OnlyLive, r)
		}
	}

	// 排序让结果稳定：不稳定的话，同样的状态每次刷新都显示成不同的顺序，
	// 人会以为「又变了」
	sort.Slice(d.WeightChanged, func(i, j int) bool {
		if d.WeightChanged[i].Line != d.WeightChanged[j].Line {
			return d.WeightChanged[i].Line < d.WeightChanged[j].Line
		}
		return d.WeightChanged[i].IP < d.WeightChanged[j].IP
	})
	sort.Slice(d.OnlyPlanned, func(i, j int) bool { return d.OnlyPlanned[i].IP < d.OnlyPlanned[j].IP })
	sort.Slice(d.OnlyLive, func(i, j int) bool { return d.OnlyLive[i].Value < d.OnlyLive[j].Value })
	return d
}
