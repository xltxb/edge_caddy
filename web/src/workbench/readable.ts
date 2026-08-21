/**
 * 可读表示 —— 工作台右栏那份 JSON。ADR-0007。
 *
 * **它不是将要下发的字节。** 真实的下发内容由主控渲染，经
 * `POST /deploys/preview` 取回，只在「校验并下发」的确认弹层里出现。
 * 这里是变更的**可读摘要**：帮人看清「我改的这一下，在配置里长什么样」。
 *
 * 术语按 CONTEXT.md：叫它「可读表示」，不要叫「预览 JSON」或「Caddy JSON 预览」——
 * 后两种叫法会让人以为它可下发。
 *
 * 移植自高保真设计稿的 caddyJSON / ruleJSON / globalJSON，字段名对齐契约 §6。
 * 有两处**没有照抄**，因为照抄会让读者对系统机制产生错误认识：
 *
 *  1. 设计稿的 mTLS 渲染成 `client_certificate_automate: 'edge-mtls'`。ADR-0008 已经
 *     否掉了这个字段——它意味着由**节点本机**的 Caddy pki app 签发客户端证书，
 *     那要求每台节点持有 CA 私钥，且 6 台节点会各自成为独立的 CA。
 *  2. 设计稿的 JWT 渲染成 `handler: 'jwtauth'`，那是 caddy-jwt 插件的 handler，
 *     我们不装它（ADR-0003）。真实机制是 Caddy 用 forward_auth 委托给 Agent 在
 *     回环上的校验端点，由 Agent 用 Go 验签。
 *
 * 摘要可以简化，但不该教人一件错的事。
 */

import type { PolicyWire, RouteWire, RuleWire } from '@/api/types'
import { normalizeLines } from '@/utils/validators'

/** 0.0.0.0/0 等于不限制，没必要渲染出一个永远不匹配的拦截段。 */
function isOpenToAll(list: string[]): boolean {
  return list.length === 1 && list[0] === '0.0.0.0/0'
}

export function routeReadable(r: RouteWire): unknown {
  const wl = normalizeLines(r.whitelist)
  const sub: unknown[] = []

  if (wl.length > 0 && !isOpenToAll(wl)) {
    sub.push({
      match: [{ not: [{ remote_ip: { ranges: wl } }] }],
      handle: [
        r.block_mode === 'abort'
          ? { handler: 'static_response', abort: true }
          : { handler: 'static_response', status_code: Number(r.block_mode) },
      ],
      terminal: true,
    })
  }

  const proxy: Record<string, unknown> = {
    handler: 'reverse_proxy',
    upstreams: [{ dial: r.upstream }],
    headers: {
      request: {
        set: {
          Host: ['{http.reverse_proxy.upstream.hostport}'],
          'X-Real-IP': ['{http.request.remote.host}'],
          'X-Forwarded-Proto': ['{http.request.scheme}'],
        },
      },
    },
    transport: {
      protocol: 'http',
      keep_alive: { max_idle_conns_per_host: 100, idle_timeout: 60000000000 },
    },
  }

  if (r.mtls) {
    // 回源 mTLS：边缘节点作为客户端向源站出示证书（ADR-0008）。
    // 证书由主控签发并经隧道下发，不是节点自己签的。
    const t = proxy.transport as Record<string, unknown>
    t.tls = { client_certificate: 'edge-mtls（主控签发，经隧道下发）' }
  }

  const handlers: unknown[] = []
  if (r.compress) handlers.push({ handler: 'encode', encodings: { zstd: {}, gzip: {} } })
  // body_max 在这里原样显示人类可读字符串。真实渲染要 int64 字节数，
  // 那个转换由主控做——这正是 ADR-0007 举的例子，也是这份表示不可下发的证据之一。
  handlers.push({ handler: 'request_body', max_size: r.body_max })
  handlers.push(proxy)
  sub.push({ handle: handlers })

  return {
    match: [{ host: [r.domain] }],
    handle: [{ handler: 'subroute', routes: sub }],
    terminal: true,
  }
}

export function ruleReadable(rule: RuleWire): unknown {
  const spec = rule.spec as Record<string, unknown>
  const common = { applied_to: rule.apply_to, enabled: rule.enabled }

  if (rule.type === 'ip_whitelist') {
    return {
      [`@${rule.id}`]: {
        not: [{ remote_ip: { ranges: normalizeLines(spec.ips as string[] | undefined) } }],
      },
      handle: [{ handler: 'static_response', abort: true }],
      ...common,
    }
  }

  // 服务密钥与 JWT 都不由 Caddy 验签：Caddy 用 forward_auth 把请求先送到
  // Agent 在回环上的校验端点，由 Agent 用 Go 真正验签，再按状态码放行或拒绝
  // （ADR-0003）。下面这个形状反映的是真实机制，不是某个插件的字段名。
  const forwardAuth = {
    handler: 'reverse_proxy',
    rewrite: { method: 'GET', uri: `/verify/${rule.type}/${rule.id}` },
    upstreams: [{ dial: '127.0.0.1:2020' }],
    handle_response: [
      { match: { status_code: [2] }, routes: [{ handle: [{ handler: 'copy_response_headers' }] }] },
      { match: { status_code: [4] }, routes: [{ handle: [{ handler: 'static_response', abort: true }] }] },
    ],
  }

  if (rule.type === 'service_secret') {
    return {
      [`@${rule.id}`]: { header: { [String(spec.header ?? '')]: ['*'] } },
      handle: [forwardAuth],
      verify_endpoint: {
        algo: spec.algo,
        ttl_s: spec.ttl_s,
        replay_protection: spec.replay_protection,
      },
      ...common,
    }
  }

  return {
    [`@${rule.id}`]: { header: { Authorization: ['Bearer *'] } },
    handle: [forwardAuth],
    verify_endpoint: {
      iss: spec.iss,
      aud: spec.aud,
      jwks_url: spec.jwks_url,
      skew_s: spec.skew_s,
    },
    ...common,
  }
}

export function policyReadable(p: PolicyWire): unknown {
  const s = p.spec as Record<string, unknown>

  if (p.id === 'tls') {
    return {
      // 签发发生在**主控**（certmagic 跑 DNS-01），不下发给节点（ADR-0001）。
      // 单独列出来，免得读者以为这一段会写进节点配置。
      master_issuance: {
        ca: s.ca === 'letsencrypt' ? "Let's Encrypt" : 'ZeroSSL',
        acme_email: s.email,
        key_type: s.key_type,
        challenge: 'dns-01',
      },
      servers_edge: {
        protocols: s.http3 ? ['h1', 'h2', 'h3'] : ['h1', 'h2'],
        tls_connection_policies: [{ protocol_min: `tls${s.min_version}` }],
        headers: s.hsts
          ? {
              response: {
                set: {
                  'Strict-Transport-Security': [
                    `max-age=${s.hsts_max_age}; includeSubDomains; preload`,
                  ],
                },
              },
            }
          : {},
        must_staple: s.ocsp,
      },
    }
  }

  const out: Record<string, unknown> = {
    logging: {
      logs: {
        edge: {
          level: s.level,
          encoder: { format: s.format },
          writer: {
            output: 'file',
            filename: '/var/log/caddy/access.log',
            roll_size_mb: s.roll_size,
            roll_keep: s.roll_keep,
          },
        },
      },
    },
    strip_server_headers: s.strip_headers,
  }
  // 关闭限流时这两个键可能根本不存在（契约 §6.3）。不渲染，也不填默认值——
  // 填了会让 diff 里凭空多出两行。
  if (s.rate_limit) {
    out.rate_limit = {
      handler: 'rate_limit',
      rate: s.rate_rps,
      burst: s.rate_burst,
      key: '{http.request.remote.host}',
    }
  }
  return out
}

/** 按 res_key 挑渲染函数，返回格式化好的多行文本。 */
export function readableFor(resKey: string, value: unknown): string {
  const kind = resKey.slice(0, resKey.indexOf(':'))
  const json =
    kind === 'route'
      ? routeReadable(value as RouteWire)
      : kind === 'rule'
        ? ruleReadable(value as RuleWire)
        : policyReadable(value as PolicyWire)
  return JSON.stringify(json, null, 2)
}
