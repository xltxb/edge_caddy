# 路由上的 mTLS 是「回源时出示客户端证书」，不是「要求访问者出示证书」

「mTLS」在这个项目里被两种读法争夺，方向完全相反。设计稿的字段说明给了定论：

> **双向 TLS (mTLS)** — 开启。**回源时携带 edge-mtls 客户端证书，源站校验后放行。**

证书页也印证：`edge-mtls (内部 CA)`，`scope: 客户端`。所以路由上的 `mtls` 指的是
**边缘节点作为客户端，向源站证明自己的身份**；访问者一侧不受影响。

## Consequences

- 渲染成 `reverse_proxy.transport.tls`，不涉及 `tls_connection_policies`。实测（Caddy 2.11.4）
  官方版接受 `pki` app 与 `client_certificate_automate`，无需插件。
- 这条读法**消除**了另一条读法带来的阻塞：若 mTLS 意味着校验访问者证书，就必须渲染
  `tls_connection_policies`，而那会让**整台 server** 转为 TLS——同节点上所有没有服务端证书的
  域名会立即失联。按正确读法，开一条路由的 mTLS 不影响任何其他域名。
- 与「要求访问者出示客户端证书」是不同的需求。真要做那个，得单独提，并且要先解决全节点
  服务端证书的覆盖问题。当前 PRD 没有这个需求。

## edge-mtls CA 的私钥放哪

设计稿用 `client_certificate_automate: 'edge-mtls'`，意味着由**节点本机的 Caddy pki app**
签发客户端证书——那要求每台节点持有 CA 私钥，且 6 台节点会各自成为独立的 CA，源站得同时信任 6 个根。
两者都不可接受，因此不采用该字段。

改为主控持根、为每个节点签发 24 小时叶子并经隧道下发，节点侧用 `client_certificate_file`
（实测官方 Caddy 2.11.4 可用，叶子 `CN=node-hk-01` 正常加载）。详见
[ADR-0009](0009-internal-pki-two-cas.md)。
