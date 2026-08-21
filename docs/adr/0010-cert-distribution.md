# ADR-0010：证书随下发内联，主控在持有证书时接管 apps/tls

- 状态：已接受
- 日期：2026-08-16
- 相关：[ADR-0001](0001-cert-issuance.md)（主控集中签发）、[ADR-0005](0005-caddy-admin-semantics.md)（逐 app POST）

## 背景

主控集中签发的证书要送到各边缘节点，并让 Caddy 真正加载它们。有两个决定要做：
证书**怎么到节点上**，以及**谁拥有 apps/tls**。

## 实测

用 Caddy 2.11.4 实际验过：

| 试的东西 | 结果 |
| --- | --- |
| `POST /config/apps/tls`，`certificates.load_pem` 内联 PEM | 200，证书可用，TLS 握手与证书链校验都通过 |
| `POST /config/apps/<name>` 而 config 里**没有 apps 键** | **500** `invalid traversal path at: config/apps/http` |
| 给 server 加 `tls_connection_policies: [{}]` | 整台 server 转 TLS |

第二条是意外发现。测试装置一直用 `{"apps":{}}` 起 Caddy，把这个情形整个盖住了——
而它正是一台刚装完官方包、Caddyfile 为空的机器的状态。

## 决定

**证书用 `load_pem` 内联，随每次下发一起带上。**

不用 `load_files` 落盘：落盘要求主控渲染的路径与节点上的实际路径一致，而那是两个
进程各自持有的知识，迟早会不一致。内联让配置自带全部内容，没有第二处需要对齐。

不是「签发那一刻推一次」：那样会让接入时间晚于签发的节点永远拿不到证书，
而现象是「那台机器上的 HTTPS 不通」——跟配置本身看起来毫无关系。

**只在主控持有证书时才渲染 apps/tls。**

一张证书都没有时完全不渲染这个 app，节点上外部证书平台写入的内容原样保留。
反过来的话，一个还没签发证书的系统会把那些内容抹掉——那是上一版真出过的事故
（ADR-0005 的由来）。

有证书时我们**接管** apps/tls：`POST /config/apps/tls` 是整体替换，没有合并的余地。
这是一个明确的所有权转移，不是疏漏。

**Agent 在 POST 单个 app 之前先确认 apps 键存在**，缺了就 `PUT /config/apps` 补一个
空对象。用 PUT 而不是 POST：POST 到已存在的键会替换它，把其它 app 抹掉。

## 代价

- 私钥出现在 Caddy 的运行配置里，能通过 Admin API 读到。Admin 只监听回环，
  且能访问它的人本来就等于拥有这台节点（部署脚本会用防火墙再兜一层），
  因此这不构成新增的暴露面。
- 每次下发的载荷会随证书数量增大。一张 ECDSA 证书链约 2KB，六个域名约 12KB，
  相对隧道的其它开销可以忽略。
- 接管 apps/tls 之后，节点上不能再有别的东西往那里写——两边会互相覆盖。

## 补充（2026-08-21）：证书要建表，`CertList` 改变含义

后端开发文档 §3 写「证书状态不建表，实时从各节点 Agent 上报的 Caddy 证书清单聚合」。
在本 ADR 下这不成立——主控是签发方，必须持有 PEM 才能内联下发，**证书必须落库**。

但 Agent 上报的 `CertList` 保留，含义改变：它不再是「有哪些证书」的**来源**
（那是主控自己的账），而是「主控下发的这些证书，节点上真的加载了吗」的**回执**。
证书页因此有两列真相：主控账面的到期时间，与各节点的实际加载情况。
两者不一致 = 下发到了但没生效，这是节点自管模型根本看不见的一类故障。

同时修正本 ADR 头部的两处链接笔误：ADR-0001 的文件名是
[0001-master-issues-certificates.md](0001-master-issues-certificates.md)，
ADR-0005 是 [0005-retry-only-transport-failures.md](0005-retry-only-transport-failures.md)。
