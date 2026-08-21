# 边缘鉴权由 Agent 校验，Caddy 用 forward_auth 委托

PRD 与后端文档 §3 要求边缘做 JWT（iss / aud / JWKS / skew）与服务密钥（HMAC + 重放窗口）
校验，但 [ADR-0001](0001-master-issues-certificates.md) 让节点跑官方 Caddy，而官方 Caddy
既没有 JWT 模块也没有 HMAC 模块（`caddy list-modules` 在 2.11.4 上只有
`http.authentication.providers.http_basic`）。决定：受保护域名的请求先经 `reverse_proxy` +
`handle_response` 委托给 **Agent 在回环上暴露的校验端点**，由 Agent 用 Go 真正验签，
Caddy 按其状态码放行或拒绝。

## Considered Options

- **自建 Caddy + caddy-jwt 插件**。进程内验签、延迟最低，但要自养构建与 CVE 跟进流程，
  且 HMAC 那一半没有现成插件，仍需自己写一个 Caddy 模块。
- **边缘只做 `header_regexp` 格式前置过滤，源站真校验**。最省事，但边缘对
  `Authorization: Bearer eyJ.x.y` 这种签名瞎编的串照样放行，只挡得住无差别扫描。

## Consequences

实测（Caddy 2.11.4，校验端点认 `Bearer good-token`）：

    无凭据          → 403
    错凭据          → 403
    正确凭据        → 200，且响应体为 "UPSTREAM sub=user-42"
    校验端点被杀后  → 502（带正确凭据也是 502）

- **验签结果能传给源站**：校验端点回的 `X-Verified-Sub` 经 Caddy 透传到了上游。源站不必
  重新解析 token，这是格式过滤方案给不了的。
- **fail-closed**：Agent 挂掉时受保护域名整体 502，不会被绕过。安全姿态正确，代价是
  Agent 可用性成为受保护域名的硬依赖——部署脚本里 Agent 的 `Restart=always` 因此不是
  锦上添花，而是承重的。
- 每个受保护请求多一次回环 HTTP 调用。同机回环，量级远小于回源，但它出现在**每个**请求上，
  JWKS 必须在 Agent 内缓存，不能每次去 IdP 取。
- 未受保护的域名不经过这条路径，不受 Agent 存活影响。
