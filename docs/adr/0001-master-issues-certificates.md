# 证书由主控集中签发，经隧道下发

PRD §4 写的是「Caddy 全生命周期自动管理（Let's Encrypt / ZeroSSL），DNS-01 校验走
Cloudflare / DNSPod」。字面照做意味着每个边缘节点都要跑一个自建 Caddy 二进制——
Caddy 的 DNS provider 全部是插件（官方包索引里 81 个 `caddy-dns/*`，无一内置），
而且每台节点上都得放一份能改写整个 zone 的 DNS API 凭据。改为：**主控**用 certmagic
跑 DNS-01，签发结果经已有的 gRPC 隧道下发，边缘节点跑 apt 装的官方 Caddy。

## Considered Options

- **每节点自建 Caddy + DNS 插件**（PRD 字面方案）。代码最少，但要自建二进制的构建、
  签名、分发与 CVE 跟进流程，且把 zone 改写权限散布到最暴露的一批机器上。
- **每节点 HTTP-01**（官方 Caddy 即可）。在这个系统里不成立：域名按权重只解析到部分
  节点，轮换外的节点无法完成校验，而节点恰恰需要在**进入轮换之前**就持有证书。

## Consequences

- DNS 服务商凭据只存在于主控一处，边缘节点被攻破不等于 zone 被改写。
- 6 个节点拿到的是同一张证书，不再有「各节点各自申请、到期时间参差」的问题。
- 主控要自己实现签发与轮换（含到期前续期、下发失败重试），这部分复杂度从 Caddy 挪到了我们身上。
- 因为主控现在真的下发服务端证书，`:443` 能正常握手，mTLS 随之可实现——上一版
  `automatic_https: disable` 且无人下发证书，导致 `:443` 长期跑在明文上、mTLS 无法落地。

## 补充（2026-08-21）：实现用 lego 而不是 certmagic

本文写的是「主控用 certmagic 跑 DNS-01」。落地时改用
[lego](https://github.com/go-acme/lego)：两者都能跑 DNS-01，而 lego **自带**
Cloudflare 与 DNSPod 的 provider 实现，certmagic 走 libdns 接口，还要再引两个
适配包。少一层适配就少一处需要跟上游对齐的地方。

本条 ADR 的结论完全不变——主控集中签发、DNS-01、DNS 凭据只存在于主控一处。
变的只是用哪个库。

**这条路径没有对真实 ACME 服务器验证过。** 验它需要一个公网可解析的域名、
一个真实的服务商账号，而且会在 CA 那边留下真实的签发记录与速率配额消耗。
围绕它的部分（存储、续期调度、内联下发、节点加载与回执）都被真 Caddy 与真
PostgreSQL 验过，签发本身是这套东西里唯一无法在本地验证的一环——
所以它被抽成了 `certs.Issuer` 接口。

首次接入时先指向 Let's Encrypt 的 **staging** 环境跑通一次：那边的速率限制
宽得多，签废了也不心疼。
