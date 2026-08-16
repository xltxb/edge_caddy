/**
 * 路由表单的即时校验。
 *
 * 规则必须与后端 internal/api/routes.go 保持一致。这份存在的唯一理由是设计稿
 * 要求即时反馈；代价是同一套规则活在两处、会慢慢漂开。漂开的两种表现都很糟：
 * 「表单说 OK 但服务端拒绝」让人一头雾水，「表单拒绝了服务端本可接受的值」
 * 则让人完全无从申诉。端到端冒烟跑的是真后端，是它们漂开时唯一会红的地方。
 */

export interface RouteDraft {
  domain: string
  upstream: string
  body_max?: string
  wl?: string[]
}

/** 字段名 → 错误文案。没有错误时该键不存在。 */
export type RouteErrors = Partial<Record<'domain' | 'upstream' | 'body_max' | 'wl', string>>

const DOMAIN_RE = /^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$/
const UPSTREAM_RE = /^[\w.-]+:\d{1,5}$/

/** humanize.ParseBytes 支持的单位（后端用的就是那个库，语义必须一致）。 */
const UNITS = new Set([
  '', 'b', 'kb', 'k', 'kib', 'ki', 'mb', 'm', 'mib', 'mi',
  'gb', 'g', 'gib', 'gi', 'tb', 't', 'tib', 'ti', 'pb', 'p', 'pib', 'pi',
  'eb', 'e', 'eib', 'ei',
])

export function validateRoute(d: RouteDraft): RouteErrors {
  const errs: RouteErrors = {}

  if (!DOMAIN_RE.test((d.domain ?? '').trim())) {
    errs.domain = '域名格式不正确，例如 api.example.com'
  }

  const upstream = (d.upstream ?? '').trim()
  if (!UPSTREAM_RE.test(upstream)) {
    // 少了端口 Caddy 会去连 80，而用户以为配的是别的端口——
    // 这类错配不报错，只会把流量默默送错地方。
    errs.upstream = '回源地址应形如 10.8.0.2:8080（必须带端口）'
  } else {
    const port = Number(upstream.slice(upstream.lastIndexOf(':') + 1))
    if (!(port >= 1 && port <= 65535)) errs.upstream = '端口应在 1–65535 之间'
  }

  if (d.body_max !== undefined && d.body_max.trim() !== '' && !isSize(d.body_max)) {
    errs.body_max = '请求体上限不是合法的大小，例如 5MB / 64MiB'
  }

  // 逐条指出是哪些非法，而不是笼统说「白名单有问题」：
  // 一屏几十条 IP 时，不指出具体哪条等于没报。
  const bad = (d.wl ?? []).map((s) => s.trim()).filter((s) => s !== '' && !isIPOrCIDR(s))
  if (bad.length) errs.wl = `不是合法的 IP 或 CIDR：${bad.join('、')}`

  return errs
}

export function isSize(s: string): boolean {
  const m = /^\s*([0-9]*\.?[0-9]+)\s*([a-zA-Z]*)\s*$/.exec(s)
  return m !== null && UNITS.has(m[2].toLowerCase())
}

export function isIPOrCIDR(s: string): boolean {
  const [addr, mask] = s.split('/')
  if (mask !== undefined) {
    const m = Number(mask)
    if (!Number.isInteger(m) || m < 0 || m > 128) return false
  }
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(addr)) {
    return addr.split('.').every((p) => p.length <= 3 && Number(p) <= 255)
  }
  // IPv6 宽松判断，最终以后端 net.ParseIP 为准
  return /^[0-9a-fA-F:]+$/.test(addr) && addr.includes(':')
}
