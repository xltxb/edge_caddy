# 工作台右栏是可读表示，真实 diff 放在确认弹层

前端文档 §5.1 把工作台右栏称作「Caddy JSON 预览」并对它做行级 diff，作为「改配置怕推错」
的主要防线。但设计稿里那份 JSON **不是可下发的配置**：

- `caddyJSON()` 输出 `{ handler: 'request_body', max_size: cfg.bodyMax }`，而 seed 里
  `bodyMax: '5MB'` 是字符串；真实 Caddy 的 `max_size` 要 int64 字节数，原样下发会被整份拒绝。
- `ruleJSON()` 输出的根本不是 Caddy 结构：`@office-wl` 命名匹配器与 `handle`、`applied_to`、
  `enabled` 平级混排，还有 `signature: {algorithm, timestamp_tolerance_s, replay_protection}`
  这样的自造字段，以及 `handler: 'jwtauth'`（caddy-jwt 插件的 handler，我们不装它，
  见 [ADR-0003](0003-edge-auth-via-agent-forward-auth.md)）。

决定：右栏保留设计稿那份可读表示（视觉与交互完全忠于设计稿），而**「校验并推送」的确认弹层
展示后端权威渲染的真实 diff**。

## Consequences

- 「所见即所发」这个性质只在确认弹层成立。右栏的 diff 是**变更的可读摘要**，不是下发内容的证明——
  代码与界面措辞都不应把它说成后者。
- 真相出现在真正要下手的那一刻，而不是平时刷屏。PRD §6.1 本来就要求确认弹层列出变更摘要，
  把权威 diff 放这里没有新增交互层级。
- 两份渲染器（前端可读表示、后端权威渲染）不要求逐字节一致，因此**不要**为它们写一致性测试——
  上一版为「前端镜像 = 后端渲染」写的那类断言在这里是错的前提。
- 后端渲染器仍需单测覆盖每种 handler 的产出形状，因为它现在是唯一的下发权威。

## 补充（2026-08-21）：权威 diff 不覆盖 apps/tls

确认弹层的权威 diff 由新端点 `POST /api/v1/deploys/preview` 提供（后端文档 §4 漏了这个
端点，由前端 agent 指出）。它渲染的是 http / logging 等**人能改的**段，**排除 apps/tls**。

理由不是省事，是两条：

- [ADR-0010](0010-cert-distribution.md) 让证书以 `load_pem` 内联，**私钥就在渲染结果里**。
  ADR-0010 论证过私钥进 Caddy 运行配置可接受（Admin 只监听回环，能访问的人本来就拥有那台机器），
  但那个论证**不覆盖浏览器**——渲染全文送进前端是一个新的暴露面（扩展、截图、错误上报），
  而 PRD §7 自己写的是「凭证只写入不回显」。
- 证书**不是草稿资源**。`config_drafts.res_key` 只有 `route:` / `rule:` / `global:` 三种前缀；
  证书由续期驱动，不由人改。「本次变更摘要」里本来就不该有它。

代价：「所见即所发」要加一个限定词。弹层底部必须标明「证书段由主控自动附加，不在此 diff 中」——
不标就是用界面措辞给出一个兑现不了的承诺，与 [ADR-0002](0002-drift-is-version-comparison.md)
对「配置漂移」的处理是同一个原则。
