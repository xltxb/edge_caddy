/**
 * 六套字段表 —— ADR-0012。
 *
 * 「三个表单组件」那个数字从来就不对：路由 1 套，访问规则按 type 分 3 套，
 * 全局策略 2 套。这里是这 6 套布局的全部内容，改字段改这里，不改组件树。
 *
 * 文案以高保真设计稿为准，但有几处按 ADR 改过，都在原地注明了。
 */

import type { PolicyWire, RouteWire, RuleWire } from '@/api/types'
import { fieldsOf, type FieldSpec } from './field-spec'
import { invalidIps, isBodyMax, isHostPort, isPositiveInt, normalizeLines } from '@/utils/validators'

/* ── 各资源的有效值类型（live 与草稿合并后的形状）── */

export type RouteDraft = RouteWire

export interface IpWhitelistRule extends RuleWire {
  type: 'ip_whitelist'
  spec: { ips: string[] }
}
export interface ServiceSecretRule extends RuleWire {
  type: 'service_secret'
  spec: { header: string; algo: string; ttl_s: number; replay_protection: boolean }
}
export interface JwtBearerRule extends RuleWire {
  type: 'jwt_bearer'
  spec: { iss: string; aud: string; jwks_url: string; skew_s: number }
}
export type RuleDraft = IpWhitelistRule | ServiceSecretRule | JwtBearerRule

export interface TlsPolicy extends PolicyWire {
  spec: {
    ca: string
    email: string
    key_type: string
    min_version: string
    http3: boolean
    hsts: boolean
    hsts_max_age: number
    ocsp: boolean
  }
}
export interface LogPolicy extends PolicyWire {
  spec: {
    format: string
    level: string
    roll_size: number
    roll_keep: number
    strip_headers: boolean
    rate_limit: boolean
    rate_rps?: number
    rate_burst?: number
  }
}

/* ── 反代路由 ── */

export const ROUTE_FIELDS = fieldsOf<RouteDraft>([
  {
    kind: 'text',
    field: 'upstream',
    label: '回源地址',
    hint: '建议走 WireGuard 内网地址，源站防火墙只放行边缘节点 IP。',
    validate: (v) => (isHostPort(v.upstream ?? '') ? null : '回源地址必须形如 host:port'),
  },
  {
    kind: 'area',
    field: 'whitelist',
    label: 'IP 白名单',
    rows: 5,
    hint: (v) =>
      `每行一个 IP 或 CIDR，共 ${normalizeLines(v.whitelist).length} 条，写入 Caddy remote_ip 匹配器。`,
    validate: (v) => {
      const bad = invalidIps(v.whitelist)
      if (bad.length === 0) return null
      return `${bad.length} 行不是合法 IP 或 CIDR：${bad.slice(0, 2).join('、')}`
    },
  },
  {
    kind: 'seg',
    field: 'block_mode',
    label: '非白名单流量处置',
    options: [
      ['abort', 'abort'],
      ['403', '403'],
      ['404', '404'],
    ],
    // 选 403 会暴露服务存在，契约 §6.1 要求前端给出这条提示
    hint: (v) =>
      v.block_mode === 'abort'
        ? 'abort 直接切断 TCP，不返回任何 HTTP 状态码，扫描器无法嗅探到应用存在。'
        : v.block_mode
          ? `返回 ${v.block_mode}，会暴露该域名后有服务在运行。`
          : '还没设置处置方式。',
  },
  {
    kind: 'switch',
    field: 'mtls',
    label: '回源 mTLS',
    // 术语按 ADR-0008：这是边缘节点回源时**出示**客户端证书，不是要求访问者出示。
    // 设计稿的关闭态文案写的是「该域名由 JWT Bearer 认证」，那是把某个域名的
    // 具体情况写死进了通用文案，换个域名就是错的。
    onText: '开启。回源时携带 edge-mtls 客户端证书，由源站校验后放行。',
    offText: '关闭。回源不出示客户端证书，访问者一侧不受影响。',
  },
  {
    kind: 'switch',
    field: 'compress',
    label: '响应压缩',
    onText: 'zstd + gzip',
    offText: '不压缩，透传上游响应',
  },
  {
    kind: 'text',
    field: 'body_max',
    label: '请求体上限',
    width: '130px',
    // 契约 §6.1：这是人类可读字符串，字节数转换是后端渲染器的事，前端不换算
    hint: '如 5MB / 64MB。单位换算由主控渲染时完成。',
    validate: (v) => (isBodyMax(v.body_max ?? '') ? null : '格式应形如 5MB / 512KB'),
  },
])

/* ── 访问规则 ── */

const ENABLED_FIELD: FieldSpec<RuleDraft> = {
  kind: 'switch',
  field: 'enabled',
  label: '启用状态',
  onText: '生效中。下发后立即参与请求匹配。',
  offText: '已停用。规则保留但不会写入节点配置。',
  offWarn: true,
}

// 空数组 = 未绑定，规则不生效。契约 §6.2 明确要求不要显示成「对所有域名生效」。
const APPLY_TO_FIELD: FieldSpec<RuleDraft> = {
  kind: 'chips',
  field: 'apply_to',
  label: '应用到',
  hint: '规则只在被勾选的域名上生效，取消后会从这些域名的配置里移除。',
  validate: (v) => (v.apply_to?.length ? null : '未绑定任何域名，这条规则不会生效'),
}

export const IP_WHITELIST_FIELDS = fieldsOf<RuleDraft>([
  ENABLED_FIELD,
  {
    kind: 'area',
    field: 'spec.ips',
    label: '允许的来源 IP',
    rows: 7,
    hint: (v) =>
      `共 ${normalizeLines((v.spec as { ips?: string[] }).ips).length} 条。非白名单流量按各域名自己的处置方式（abort / 403）处理。`,
    validate: (v) => {
      const bad = invalidIps((v.spec as { ips?: string[] }).ips)
      if (bad.length === 0) return null
      return `${bad.length} 行不是合法 IP 或 CIDR：${bad.slice(0, 2).join('、')}`
    },
  },
  APPLY_TO_FIELD,
])

export const SERVICE_SECRET_FIELDS = fieldsOf<RuleDraft>([
  ENABLED_FIELD,
  {
    kind: 'text',
    field: 'spec.header',
    label: '密钥请求头',
    hint: '第三方系统在此头里携带签名，缺失或不匹配的请求按域名处置方式丢弃。',
  },
  {
    kind: 'seg',
    field: 'spec.algo',
    label: '签名算法',
    options: [
      ['hmac-sha256', 'HMAC-SHA256'],
      ['hmac-sha512', 'HMAC-SHA512'],
      ['ed25519', 'Ed25519'],
    ],
    // 验签由 Agent 的校验端点用 Go 做，不是 Caddy 插件（ADR-0003）
    hint: '由边缘节点上的 Agent 验签，Caddy 通过 forward_auth 委托给它。',
  },
  {
    kind: 'text',
    field: 'spec.ttl_s',
    label: '签名有效期（秒）',
    width: '130px',
    numeric: true,
    validate: (v) =>
      isPositiveInt((v.spec as { ttl_s?: number }).ttl_s) ? null : '必须是正整数',
  },
  {
    kind: 'switch',
    field: 'spec.replay_protection',
    label: '重放保护',
    onText: '同一签名在有效期内只接受一次。',
    offText: '不做重放检查，有效期内的签名可被重复使用。',
    offWarn: true,
  },
  APPLY_TO_FIELD,
])

export const JWT_BEARER_FIELDS = fieldsOf<RuleDraft>([
  ENABLED_FIELD,
  { kind: 'text', field: 'spec.iss', label: '签发者 (iss)' },
  { kind: 'text', field: 'spec.aud', label: '受众 (aud)' },
  {
    kind: 'text',
    field: 'spec.jwks_url',
    label: 'JWKS 地址',
    hint: '边缘节点缓存公钥，密钥轮换后最长 5 分钟生效。',
  },
  {
    kind: 'text',
    field: 'spec.skew_s',
    label: '时钟偏移容差（秒）',
    width: '130px',
    numeric: true,
    validate: (v) =>
      isPositiveInt((v.spec as { skew_s?: number }).skew_s) ? null : '必须是正整数',
  },
  APPLY_TO_FIELD,
])

/* ── 全局策略 ── */

// 契约 §6.3：前三个字段是**主控**签发证书时用的参数，不下发给节点。
// 不分组的话，人会以为改 email 也要走一次全网下发。
const G_ISSUE = '主控签发参数（不下发给节点）'
const G_NODE = '下发到节点的 TLS 配置'

export const TLS_FIELDS = fieldsOf<TlsPolicy>([
  {
    kind: 'seg',
    field: 'spec.ca',
    label: '证书颁发机构',
    group: G_ISSUE,
    // 设计稿原文是「Caddy 全生命周期自动申请与续期」——与 ADR-0001 矛盾：
    // 签发在主控，边缘节点跑官方 Caddy，不自己申请证书。
    hint: '主控用 certmagic 跑 DNS-01 集中签发，边缘节点不申请证书。',
    options: [
      ['letsencrypt', "Let's Encrypt"],
      ['zerossl', 'ZeroSSL'],
    ],
  },
  {
    kind: 'text',
    field: 'spec.email',
    label: 'ACME 账户邮箱',
    group: G_ISSUE,
    hint: '证书到期前的通知会发到这里。',
  },
  {
    kind: 'seg',
    field: 'spec.key_type',
    label: '密钥算法',
    group: G_ISSUE,
    options: [
      ['p256', 'ECDSA P-256'],
      ['p384', 'ECDSA P-384'],
      ['rsa2048', 'RSA 2048'],
    ],
  },
  {
    kind: 'seg',
    field: 'spec.min_version',
    label: '最低 TLS 版本',
    group: G_NODE,
    options: [
      ['1.2', 'TLS 1.2'],
      ['1.3', 'TLS 1.3'],
    ],
    // 没设置时不要落进「1.2」那一支 —— 那等于声称 1.2 正在生效
    hint: (v) =>
      v.spec.min_version === '1.3'
        ? '仅 TLS 1.3。安全性最高，但会挡掉部分老旧客户端。'
        : v.spec.min_version === '1.2'
          ? '兼容 TLS 1.2，覆盖面更广。'
          : '还没设置，节点会用 Caddy 的默认值。',
  },
  {
    kind: 'switch',
    field: 'spec.http3',
    label: 'HTTP/3 (QUIC)',
    group: G_NODE,
    onText: '开启。需在防火墙放行 443/udp。',
    offText: '关闭。弱网环境下首包延迟会高于 QUIC。',
  },
  {
    kind: 'switch',
    field: 'spec.hsts',
    label: 'HSTS',
    group: G_NODE,
    // 没配过时不要打印 undefined —— 界面上出现 undefined 永远是错的
    onText: (v) =>
      v.spec.hsts_max_age
        ? `max-age=${v.spec.hsts_max_age}（约 ${Math.round(v.spec.hsts_max_age / 86400)} 天）· includeSubDomains · preload`
        : '已开启，但还没设 max-age。',
    offText: '不下发 Strict-Transport-Security 响应头。',
  },
  {
    kind: 'text',
    field: 'spec.hsts_max_age',
    label: 'HSTS max-age（秒）',
    group: G_NODE,
    width: '160px',
    numeric: true,
    visible: (v) => v.spec.hsts === true,
  },
  {
    kind: 'switch',
    field: 'spec.ocsp',
    label: 'OCSP Must-Staple',
    group: G_NODE,
    onText: '开启后 OCSP 响应器故障会导致握手失败，谨慎使用。',
    offText: '关闭。由客户端自行查询 OCSP。',
  },
])

export const LOG_FIELDS = fieldsOf<LogPolicy>([
  {
    kind: 'seg',
    field: 'spec.format',
    label: '日志格式',
    options: [
      ['json', 'json'],
      ['console', 'console'],
    ],
  },
  {
    kind: 'seg',
    field: 'spec.level',
    label: '日志级别',
    options: [
      ['DEBUG', 'DEBUG'],
      ['INFO', 'INFO'],
      ['WARN', 'WARN'],
      ['ERROR', 'ERROR'],
    ],
  },
  {
    kind: 'text',
    field: 'spec.roll_size',
    label: '单文件滚动大小（MB）',
    width: '130px',
    numeric: true,
  },
  {
    kind: 'text',
    field: 'spec.roll_keep',
    label: '保留文件数',
    width: '130px',
    numeric: true,
    hint: (v) =>
      `当前配置下每个节点最多占用 ${(v.spec.roll_size ?? 0) * (v.spec.roll_keep ?? 0)} MB 磁盘。`,
  },
  {
    kind: 'switch',
    field: 'spec.strip_headers',
    label: '剥离识别指纹',
    onText: '移除 Server 与 X-Powered-By 响应头。',
    offText: '保留默认响应头。',
  },
  {
    kind: 'switch',
    field: 'spec.rate_limit',
    label: '请求限流',
    onText: '按来源 IP 限速。',
    offText: '不限流。',
  },
  // 条件字段：契约 §6.3 说 rate_limit=false 时这两个键可能根本不存在，
  // 关闭时不渲染，也不要偷偷填默认值——那会让 diff 里凭空多出两行。
  {
    kind: 'text',
    field: 'spec.rate_rps',
    label: '每秒请求数',
    width: '130px',
    numeric: true,
    visible: (v) => v.spec.rate_limit === true,
  },
  {
    kind: 'text',
    field: 'spec.rate_burst',
    label: '突发上限',
    width: '130px',
    numeric: true,
    visible: (v) => v.spec.rate_limit === true,
  },
])

/** 按资源 key 与其有效值挑出该用哪张表。 */
export function fieldsFor(resKey: string, value: unknown): FieldSpec<never>[] {
  const kind = resKey.slice(0, resKey.indexOf(':'))
  if (kind === 'route') return ROUTE_FIELDS as FieldSpec<never>[]
  if (kind === 'rule') {
    const t = (value as RuleWire).type
    if (t === 'service_secret') return SERVICE_SECRET_FIELDS as FieldSpec<never>[]
    if (t === 'jwt_bearer') return JWT_BEARER_FIELDS as FieldSpec<never>[]
    return IP_WHITELIST_FIELDS as FieldSpec<never>[]
  }
  const id = resKey.slice(resKey.indexOf(':') + 1)
  return (id === 'tls' ? TLS_FIELDS : LOG_FIELDS) as FieldSpec<never>[]
}
