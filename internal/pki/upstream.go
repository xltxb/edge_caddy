package pki

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// UpstreamTTL 是回源客户端证书的有效期（docs/adr/0009）。
//
// 做短是因为吊销靠过期：内部 PKI 的 CRL/OCSP 基本没人真部署，写了也是摆设——
// 一个不会被查询的吊销列表等于没有吊销。把叶子做短，吊销就退化成
// 「停止续期」这一个动作，不需要新机制。
const UpstreamTTL = 24 * time.Hour

// renewFraction 决定何时续期：剩余寿命低于总寿命的这个比例时换新。
//
// 太晚续会让一次续期失败直接导致过期；太早续等于把 24 小时的吊销窗口又拉长。
const renewFraction = 3

// UpstreamIssuer 管理各节点的回源客户端证书。
//
// 证书由**主控**签发，节点上不存在任何 CA 私钥——节点被攻破时攻击者拿到的是
// 一张 24 小时后作废的叶子，不是签发权（docs/adr/0009）。
type UpstreamIssuer struct {
	ca  *CA
	now func() time.Time

	mu      sync.Mutex
	held    map[string]Issued
	revoked map[string]bool
}

func NewUpstreamIssuer(ca *CA, now func() time.Time) *UpstreamIssuer {
	if now == nil {
		now = time.Now
	}
	return &UpstreamIssuer{
		ca: ca, now: now,
		held: map[string]Issued{}, revoked: map[string]bool{},
	}
}

// EnsureFor 返回该节点当前可用的回源证书，必要时签发或续期。
func (u *UpstreamIssuer) EnsureFor(_ context.Context, nodeID string) (Issued, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.revoked[nodeID] {
		return Issued{}, fmt.Errorf("节点 %s 的回源凭据已吊销", nodeID)
	}
	if cur, ok := u.held[nodeID]; ok && !u.needsRenew(cur) {
		// 未到续期窗口就复用：每次都重签意味着每次下发都换证书，而换证书要
		// 重载 Caddy——一次无关的配置下发会顺带打断所有回源连接。
		return cur, nil
	}

	issued, err := u.ca.IssueClient(nodeID, UpstreamTTL)
	if err != nil {
		return Issued{}, fmt.Errorf("签发 %s 的回源证书: %w", nodeID, err)
	}
	u.held[nodeID] = issued
	return issued, nil
}

func (u *UpstreamIssuer) needsRenew(is Issued) bool {
	remaining := is.NotAfter.Sub(u.now())
	return remaining < UpstreamTTL/renewFraction
}

// Revoke 停止为某节点续期。它手上那张证书会在 24 小时内自然失效。
//
// 没有 CRL 也没有 OCSP：见 UpstreamTTL 的说明。代价是吊销不是立刻生效，
// 最坏要等一个 TTL——这是「短寿命换掉复杂度」这笔交易的另一半，
// 必须清楚它不是即时的。
func (u *UpstreamIssuer) Revoke(nodeID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.revoked[nodeID] = true
	delete(u.held, nodeID)
}

// RootPEM 返回回源 CA 的根证书，供各源站放进信任库。
func (u *UpstreamIssuer) RootPEM() []byte { return u.ca.RootPEM() }

// ExpiryOf 返回某节点当前证书的到期时间，供监控与界面展示。
func (u *UpstreamIssuer) ExpiryOf(nodeID string) (time.Time, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	is, ok := u.held[nodeID]
	if !ok {
		return time.Time{}, false
	}
	return is.NotAfter, true
}
