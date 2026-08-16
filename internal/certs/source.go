package certs

import "context"

// Source 给下发编排提供当前全部证书。
//
// 证书**随每次下发一起带上**，而不是「签发那一刻推一次」：后者会让接入时间
// 晚于签发的节点永远拿不到证书，而现象是「那台机器上的 HTTPS 不通」，
// 跟配置本身看起来毫无关系。
type Source struct {
	st     Store
	master []byte
}

func NewSource(st Store, master []byte) *Source {
	return &Source{st: st, master: master}
}

// Certs 返回全部证书。
func (s *Source) Certs(ctx context.Context) ([]Cert, error) {
	return All(ctx, s.st, s.master)
}

// Domains 返回已有证书的域名，供续期巡检使用。
func (s *Source) Domains(ctx context.Context) ([]string, error) {
	all, err := All(ctx, s.st, s.master)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for _, c := range all {
		out = append(out, c.Domain)
	}
	return out, nil
}
