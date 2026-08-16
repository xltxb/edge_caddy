package certs

import (
	"sort"
	"sync"
	"time"
)

// StaleAfter 是判定「这份数据已经旧了」的阈值。
//
// 节点每 3 秒一次心跳，探活也会带回清单。超过 10 分钟没更新，说明这台机器
// 已经掉了一阵子——它上报的证书状态就是那时候的。
const StaleAfter = 10 * time.Minute

// 三档着色的分界。分档由后端给，前端不自己算：前端自己算的话，
// 「什么算紧急」就有了两个定义，改一处忘一处。
const (
	CritBelowDays = 7
	WarnBelowDays = 30
)

// NodeCert 是节点上报的一张证书。
type NodeCert struct {
	Domain   string    `json:"domain"`
	NotAfter time.Time `json:"not_after"`
	Issuer   string    `json:"issuer"`
	KeyType  string    `json:"key_type"`
	Serial   string    `json:"serial"`
}

// NodeReport 是一个节点某一时刻的完整证书清单。
type NodeReport struct {
	NodeID string
	At     time.Time
	Certs  []NodeCert
}

// Aggregated 是一个域名在全网的聚合结果。
type Aggregated struct {
	Domain    string    `json:"domain"`
	NotAfter  time.Time `json:"not_after"`
	DaysLeft  int       `json:"days_left"`
	Severity  string    `json:"severity"`
	Issuer    string    `json:"issuer"`
	KeyType   string    `json:"key_type"`
	NodeCount int       `json:"node_count"`
	// HasStale 表示参与聚合的数据里有已经陈旧的。
	HasStale bool `json:"has_stale"`
	// StaleNodes 说清楚是哪几台——否则运维不知道去查哪台机器。
	StaleNodes      []string      `json:"stale_nodes"`
	OldestReportAge time.Duration `json:"-"`
	OldestAgeSec    int64         `json:"oldest_age_sec"`
}

// Inventory 是证书状态的内存视图。
//
// **不落库**（PRD §4）：落库只会得到一份随时可能过时的副本，而「过时的证书
// 状态」比没有更危险——它会让人以为一张已经换掉的证书还在生效。
//
// 主控重启后视图为空，等节点下一次上报即恢复；那几秒的空白比一份陈旧数据
// 诚实得多。
type Inventory struct {
	now func() time.Time

	mu      sync.RWMutex
	reports map[string]NodeReport
}

func NewInventory(now func() time.Time) *Inventory {
	if now == nil {
		now = time.Now
	}
	return &Inventory{now: now, reports: map[string]NodeReport{}}
}

// Report 登记一个节点的清单，**替换**该节点此前的上报。
//
// 追加的话，续期换掉的旧证书会一直留在聚合里，而旧证书的到期时间更早——
// 于是面板永远显示「即将过期」，直到没人再看它。
func (i *Inventory) Report(r NodeReport) {
	if r.At.IsZero() {
		r.At = i.now()
	}
	i.mu.Lock()
	i.reports[r.NodeID] = r
	i.mu.Unlock()
}

// Forget 丢掉某个节点的上报。节点下线或被删除时调用——
// 否则一台已经拆掉的机器会永远拉低聚合结果。
func (i *Inventory) Forget(nodeID string) {
	i.mu.Lock()
	delete(i.reports, nodeID)
	i.mu.Unlock()
}

// Aggregate 把各节点的上报聚合成按域名的视图，按剩余天数升序。
func (i *Inventory) Aggregate() []Aggregated {
	now := i.now()
	i.mu.RLock()
	reports := make([]NodeReport, 0, len(i.reports))
	for _, r := range i.reports {
		reports = append(reports, r)
	}
	i.mu.RUnlock()

	type acc struct {
		agg    Aggregated
		nodes  map[string]bool
		stale  map[string]bool
		oldest time.Time
	}
	byDomain := map[string]*acc{}

	for _, r := range reports {
		isStale := now.Sub(r.At) > StaleAfter
		for _, c := range r.Certs {
			a, seen := byDomain[c.Domain]
			if !seen {
				a = &acc{
					agg:   Aggregated{Domain: c.Domain, NotAfter: c.NotAfter, Issuer: c.Issuer, KeyType: c.KeyType},
					nodes: map[string]bool{}, stale: map[string]bool{},
					oldest: r.At,
				}
				byDomain[c.Domain] = a
			}
			// **取最早**：取最晚会让「有个节点的证书明天就过期」被一个 90 天的
			// 副本掩盖，而那个节点明天就会开始拒绝连接
			if c.NotAfter.Before(a.agg.NotAfter) {
				a.agg.NotAfter = c.NotAfter
				a.agg.Issuer, a.agg.KeyType = c.Issuer, c.KeyType
			}
			a.nodes[r.NodeID] = true
			if isStale {
				a.stale[r.NodeID] = true
			}
			if r.At.Before(a.oldest) {
				a.oldest = r.At
			}
		}
	}

	out := make([]Aggregated, 0, len(byDomain))
	for _, a := range byDomain {
		g := a.agg
		g.NodeCount = len(a.nodes)
		g.DaysLeft = int(g.NotAfter.Sub(now).Hours() / 24)
		g.Severity = severityOf(g.NotAfter, now)
		g.HasStale = len(a.stale) > 0
		g.StaleNodes = sortedKeys(a.stale)
		g.OldestReportAge = now.Sub(a.oldest)
		g.OldestAgeSec = int64(g.OldestReportAge / time.Second)
		out = append(out, g)
	}
	sort.Slice(out, func(x, y int) bool {
		if out[x].NotAfter.Equal(out[y].NotAfter) {
			return out[x].Domain < out[y].Domain
		}
		return out[x].NotAfter.Before(out[y].NotAfter)
	})
	return out
}

func severityOf(notAfter, now time.Time) string {
	days := notAfter.Sub(now).Hours() / 24
	switch {
	case days < CritBelowDays:
		return "crit"
	case days < WarnBelowDays:
		return "warn"
	default:
		return "ok"
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
