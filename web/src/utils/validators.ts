/**
 * 输入校验 —— 只做**前端能确定**的那部分。
 *
 * 真正的权威校验在主控的 Go 层（ADR-0004：主控不装 Caddy，不跑 caddy validate），
 * 结果经 `POST /deploys/preview` 的 `validation.errors` 回来。这里做的是即时反馈：
 * 让人在输入框里就看见「这行不是合法 IP」，而不是点了下发才知道。
 *
 * 因此这里宁可**严**不可松：放过一个非法值，代价是它一路走到下发才被拒；
 * 而误判一个合法值，人当场就会发现并告诉我们。
 */

const OCTET = /^\d{1,3}$/

function isOctet(s: string): boolean {
  if (!OCTET.test(s)) return false
  // 前导零会被某些解析器按八进制读，含义会变，一律不接受
  if (s.length > 1 && s.startsWith('0')) return false
  const n = Number(s)
  return n >= 0 && n <= 255
}

/** 合法 IPv4，如 `203.0.113.7`。 */
export function isIpv4(s: string): boolean {
  const parts = s.split('.')
  return parts.length === 4 && parts.every(isOctet)
}

/**
 * 合法 IPv4 或 CIDR，如 `10.8.0.0/24`。
 *
 * 设计稿用的是 `^(\d{1,3}\.){3}\d{1,3}(\/\d{1,2})?$`，它会放过
 * `999.999.999.999/99` —— 那种值会一路走到 Caddy 才被拒，而那时它已经
 * 混在一次全网下发里了。
 */
export function isIpOrCidr(s: string): boolean {
  const [addr, prefix, ...extra] = s.split('/')
  if (extra.length > 0 || addr === undefined) return false
  if (!isIpv4(addr)) return false
  if (prefix === undefined) return true
  if (!/^\d{1,2}$/.test(prefix)) return false
  if (prefix.length > 1 && prefix.startsWith('0')) return false
  const n = Number(prefix)
  return n >= 0 && n <= 32
}

/** 把多行文本按行规范化：去首尾空白、丢掉空行。等值比较前必须过这一步。 */
export function normalizeLines(list: readonly string[] | undefined): string[] {
  return (list ?? []).map((x) => String(x).trim()).filter(Boolean)
}

/** 返回非法的那些行（原值），供输入框下方逐条报出来。 */
export function invalidIps(list: readonly string[] | undefined): string[] {
  return normalizeLines(list).filter((x) => !isIpOrCidr(x))
}

/** 回源地址必须形如 `host:port`。 */
export function isHostPort(s: string): boolean {
  const i = s.lastIndexOf(':')
  if (i <= 0 || i === s.length - 1) return false
  const host = s.slice(0, i)
  const port = s.slice(i + 1)
  if (!/^\d{1,5}$/.test(port)) return false
  const p = Number(port)
  if (p < 1 || p > 65535) return false
  if (isIpv4(host)) return true
  // 主机名：字母数字与连字符，点分，各段不以连字符开头结尾
  return /^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$/.test(
    host,
  )
}

/** 域名。允许通配符前缀 `*.example.com`。 */
export function isDomain(s: string): boolean {
  const body = s.startsWith('*.') ? s.slice(2) : s
  if (body.length === 0 || body.length > 253) return false
  if (!body.includes('.')) return false
  return /^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$/.test(
    body,
  )
}

/**
 * 请求体上限，如 `5MB`。
 *
 * 注意这是**人类可读字符串**，不是可下发的值 —— Caddy 的 `max_size` 要 int64
 * 字节数，那个转换由后端渲染器做（契约 §6.1）。前端只校验格式，不做单位换算。
 */
export function isBodyMax(s: string): boolean {
  return /^\d+(\.\d+)?\s*(B|KB|MB|GB)$/i.test(s.trim())
}

/** 正整数，用于 ttl / skew / rps 这类字段。 */
export function isPositiveInt(v: unknown): boolean {
  const n = typeof v === 'number' ? v : Number(String(v).trim())
  return Number.isInteger(n) && n > 0
}
