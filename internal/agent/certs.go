package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"time"

	edgev1 "github.com/xltxb/edge_caddy/gen/edge/v1"
)

// certReceipt 是「这台机器上，这个域名的 TLS 实际出示的证书」。
type certReceipt struct {
	Domain      string
	NotAfter    time.Time
	Fingerprint string
}

// collectCertReceipts 逐个域名在**本机回环上真握一次手**，读对端出示的证书。
//
// 为什么不直接复述主控下发的那份：那只能证明「我收到了这些 PEM」，
// 而契约要回答的是「下发到了之后节点有没有真的加载」。这两件事不一样——
// ADR-0004 复核时那个「幽灵监听」已经教过一次了：配置被接受不等于流量在走。
//
// 真握手能查出配置被接受、TLS app 却没生效的情形，而那正是节点自管证书的
// 模型根本看不见的一类故障。
func collectCertReceipts(ctx context.Context, tlsAddr string, domains []string, log *slog.Logger) []certReceipt {
	out := make([]certReceipt, 0, len(domains))
	for _, domain := range domains {
		r, err := probeCert(ctx, tlsAddr, domain)
		if err != nil {
			// 握不上就**不报**这个域名。报一个空回执会让证书页显示「加载了」
			// 而实际没有——那正是这个字段要拆穿的东西。
			log.Debug("证书回执探测失败", "domain", domain, "err", err)
			continue
		}
		out = append(out, r)
	}
	return out
}

func probeCert(ctx context.Context, tlsAddr, domain string) (certReceipt, error) {
	network, address := "tcp", tlsAddr
	if p, ok := strings.CutPrefix(tlsAddr, "unix/"); ok {
		network, address = "unix", p
	}

	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	raw, err := (&net.Dialer{}).DialContext(dialCtx, network, address)
	if err != nil {
		return certReceipt{}, err
	}
	defer raw.Close()

	// InsecureSkipVerify：我们**不是**在校验这张证书可不可信，而是在读
	// 「对端到底出示了什么」。可信与否由源站/浏览器判断，不是这里的事。
	conn := tls.Client(raw, &tls.Config{
		ServerName:         domain,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	defer conn.Close()

	if err := conn.HandshakeContext(dialCtx); err != nil {
		return certReceipt{}, err
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return certReceipt{}, errNoPeerCert
	}
	sum := sha256.Sum256(certs[0].Raw)
	return certReceipt{
		Domain:      domain,
		NotAfter:    certs[0].NotAfter,
		Fingerprint: hex.EncodeToString(sum[:]),
	}, nil
}

var errNoPeerCert = &noPeerCertError{}

type noPeerCertError struct{}

func (*noPeerCertError) Error() string { return "对端没有出示任何证书" }

// certDomainsOf 从下发的配置里读出内联了哪些证书。
// tags 是渲染器写进去的域名（见 render.tlsApp）。
func certDomainsOf(caddyJSON []byte) []string {
	var cfg struct {
		Apps struct {
			TLS struct {
				Certificates struct {
					LoadPEM []struct {
						Tags []string `json:"tags"`
					} `json:"load_pem"`
				} `json:"certificates"`
			} `json:"tls"`
		} `json:"apps"`
	}
	if json.Unmarshal(caddyJSON, &cfg) != nil {
		return nil
	}
	var out []string
	for _, p := range cfg.Apps.TLS.Certificates.LoadPEM {
		out = append(out, p.Tags...)
	}
	return out
}

func toProtoCerts(rs []certReceipt) *edgev1.CertList {
	entries := make([]*edgev1.CertEntry, 0, len(rs))
	for _, r := range rs {
		entries = append(entries, &edgev1.CertEntry{
			Domain:       r.Domain,
			NotAfterUnix: r.NotAfter.Unix(),
			Fingerprint:  r.Fingerprint,
		})
	}
	return &edgev1.CertList{Entries: entries}
}
