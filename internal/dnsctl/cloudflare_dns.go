package dnsctl

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"

	"github.com/xltxb/edge_caddy/internal/dnssched"
)

// CloudflareDNS 用**普通 A / AAAA 记录**做轮换，不碰 Load Balancing。
//
// # 它存在的理由
//
// Load Balancing 是 Cloudflare 的付费附加产品。没开通的话，上面那个
// Cloudflare 适配一步都走不了——灰度上撞到的原话是
// `10000 Authentication error`，而那句话不会告诉人「这个产品你没买」。
//
// # 它做得到什么，做不到什么
//
//	做得到  节点进出轮换：挂了自动摘、下线时真的移出解析
//	做不到  权重、地域分流
//
// **前一半才是这套系统里最要命的那半。** 一台死掉的机器还留在解析里，
// 意味着一部分用户直接打不开；而权重分配不均只是效率问题。
//
// # 塌缩要报错，不能默默取平均
//
// 普通 DNS 记录既没有权重也没有线路。所以五条线必须配同一组节点、
// 且权重必须全部相同——不一致时**明确报错**。
//
// 取个平均或者挑一条线用，会给出一个用户没要过的配置，而且没人会发现：
// 界面上权重条画着 60/40，而实际是轮询。跟上面那个适配拒绝
// 「电信/联通/移动不一致」是同一条理由。
type CloudflareDNS struct {
	cfAPI

	ZoneID   string
	Hostname string
}

func NewCloudflareDNS(zoneID, hostname string) *CloudflareDNS {
	return &CloudflareDNS{cfAPI: newCFAPI(), ZoneID: zoneID, Hostname: hostname}
}

func (c *CloudflareDNS) Caps() Caps {
	return Caps{
		Kind: "cloudflare_dns",
		// 一条线盖住全部五条：界面据此把五个输入框合成一个，
		// 于是「五条线配得不一样」这个会被拒绝的状态在界面上造不出来。
		Lines: []LineCap{
			{Code: "all", Name: "全部线路（普通 DNS 记录分不出线路）",
				Covers: []string{"ct", "cu", "cm", "tw", "ov"}},
		},
		Weights: false,
		Notes: "用普通 A / AAAA 记录轮换，不需要 Load Balancing（那是付费附加产品）。" +
			"节点挂掉会自动移出解析、下线时也会真的摘掉——而「权重」和「地域分流」做不到：" +
			"DNS 记录既没有权重字段也没有线路概念，多条记录是等概率轮询。" +
			"要按权重或地域分流，得开通 Load Balancing 或改用 DNSPod。",
	}
}

// Sync 把这个域名的 A / AAAA 记录对成计划里的那组 IP。
func (c *CloudflareDNS) Sync(ctx context.Context, plan dnssched.Plan) error {
	want, err := collapseToPlainRotation(plan)
	if err != nil {
		return err
	}
	if len(want) == 0 {
		// **不清空记录。** 把最后一条记录撤掉等于主动让域名解析不出来，
		// 而「一个节点都不在轮换里」多半是一次短暂的全体离线。
		// 宁可让流量继续打到已知的机器上，也不要主动制造一次 NXDOMAIN。
		// 与 Load Balancing 那个适配同一条规矩。
		return capErr("没有任何节点在解析轮换里，本次不改动 DNS 记录")
	}
	return c.reconcile(ctx, want)
}

// collapseToPlainRotation 把五条线塌缩成一组 IP。不一致时报错。
func collapseToPlainRotation(plan dnssched.Plan) ([]string, error) {
	byLine := map[string][]dnssched.Entry{}
	for _, lp := range plan.Lines {
		byLine[lp.Code] = plan.Rotation(lp.Code)
	}

	all := []string{"ct", "cu", "cm", "tw", "ov"}
	ref := signature(byLine[all[0]])
	for _, l := range all[1:] {
		if signature(byLine[l]) != ref {
			return nil, capErr(
				"普通 DNS 记录分不出线路，五条线必须配置相同的节点与权重，"+
					"当前 %s 与 %s 不一致。要按线路分流得改用 DNSPod",
				lineName(all[0]), lineName(l))
		}
	}

	entries := byLine[all[0]]
	// **权重也要一致。** 多条 A 记录是等概率轮询，60/40 表达不出来。
	// 默默按等权处理的话，界面上权重条画着 60/40 而实际是 50/50——
	// 而那种不一致没有任何地方会说出来。
	for i, e := range entries {
		if i > 0 && e.Weight != entries[0].Weight {
			return nil, capErr(
				"普通 DNS 记录没有权重字段，多条记录是等概率轮询，"+
					"所以各节点的权重必须相同（当前 %s=%d、%s=%d）。"+
					"要按权重分流得开通 Cloudflare Load Balancing，或改用 DNSPod",
				entries[0].Node, entries[0].Weight, e.Node, e.Weight)
		}
	}

	ips := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IP != "" {
			ips = append(ips, e.IP)
		}
	}
	sort.Strings(ips)
	return ips, nil
}

type cfDNSRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	// Proxied **必须是 false**。
	//
	// 打开橙云的话，Cloudflare 会代理这个域名的流量——那时候到达用户的是
	// Cloudflare 的边缘，不是我们的节点，而这套系统就成了一个没人经过的摆设。
	// 更难查的是它**看起来是好的**：域名能打开、证书也正常。
	Proxied bool `json:"proxied"`
}

func (c *CloudflareDNS) reconcile(ctx context.Context, want []string) error {
	have, err := c.list(ctx)
	if err != nil {
		return err
	}

	// 按「类型+内容」对账，不按记录 ID：ID 是 Cloudflare 那边的，
	// 我们这边没有它的稳定来源。
	haveByContent := map[string]cfDNSRecord{}
	for _, r := range have {
		haveByContent[r.Content] = r
	}
	wantSet := map[string]bool{}
	for _, ip := range want {
		wantSet[ip] = true
	}

	// **先加后删。** 反过来的话，中间会有一个所有旧记录都没了、
	// 新记录还没建上的窗口，那期间域名解析不出来。
	for _, ip := range want {
		if _, ok := haveByContent[ip]; ok {
			continue
		}
		rec := cfDNSRecord{
			Type: recordType(ip), Name: c.Hostname, Content: ip,
			TTL: 60, Proxied: false,
		}
		if err := c.call(ctx, http.MethodPost,
			"/zones/"+c.ZoneID+"/dns_records", rec, nil); err != nil {
			return fmt.Errorf("新增记录 %s: %w", ip, err)
		}
	}
	for _, r := range have {
		if wantSet[r.Content] {
			continue
		}
		if err := c.call(ctx, http.MethodDelete,
			"/zones/"+c.ZoneID+"/dns_records/"+r.ID, nil, nil); err != nil {
			return fmt.Errorf("删除记录 %s: %w", r.Content, err)
		}
	}
	return nil
}

// list 读出这个域名当前的 A / AAAA 记录。
//
// **只看这两种类型**：同名的 TXT / CAA 之类不归我们管，
// 而把它们一起删掉是一次没人要求过的破坏。
func (c *CloudflareDNS) list(ctx context.Context) ([]cfDNSRecord, error) {
	var out []cfDNSRecord
	for _, typ := range []string{"A", "AAAA"} {
		// call 把**整个信封**解进 out，所以这里要 struct{Result ...}
		// 而不是直接给一个切片——给切片的话解不动，报的是一句
		// 「cannot unmarshal object into Go value of type []...」，
		// 而那句话不会让人想到「信封」两个字。
		var page struct{ Result []cfDNSRecord }
		if err := c.call(ctx, http.MethodGet,
			"/zones/"+c.ZoneID+"/dns_records?type="+typ+"&name="+c.Hostname,
			nil, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Result...)
	}
	return out, nil
}

func recordType(ip string) string {
	if p := net.ParseIP(ip); p != nil && p.To4() == nil {
		return "AAAA"
	}
	return "A"
}
