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
