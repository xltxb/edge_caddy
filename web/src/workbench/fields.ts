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
/**
 * 黑名单。**跟白名单是两个类型，而 spec 一模一样** —— 区别全在 `type` 上。
 *
 * 这一点在字段表这一层特别危险：`fieldsFor` 从前对未知 type 兜底成
 * 白名单那张表，而黑名单的字段恰好也叫 `ips` —— **表单会正常工作，
 * 每一句文案都在说「允许的来源」，而人正在编辑一条拦截规则。**
 * 兜底已经改掉，见 `fieldsFor`。
 */
export interface IpBlacklistRule extends RuleWire {
  type: 'ip_blacklist'
  spec: { ips: string[] }
}
export interface RequestFilterRule extends RuleWire {
  type: 'request_filter'
  spec: { filters: { field: string; op: string; value: string; name?: string }[] }
}
export interface RateLimitRule extends RuleWire {
  type: 'rate_limit'
  spec: { requests: number; window_s: number; rate_key: string }
}
export interface GeoBlockRule extends RuleWire {
  type: 'geo_block'
  spec: { geo_mode: string; geo_countries: string[] }
}
export type RuleDraft =
  | IpWhitelistRule
  | IpBlacklistRule
  | RequestFilterRule
  | RateLimitRule
  | GeoBlockRule
  | ServiceSecretRule
  | JwtBearerRule

export interface TlsPolicy extends PolicyWire {
  spec: {
    /*
     * **没有 ca / email / key_type。** 那三项是主控用 ACME 签发证书时的参数，
     * 而主控不再签发（ADR-0015、契约 §9）—— 后端把它们从响应里删了。
     *
     * 它们比一个恒为 false 的字段更坏：**那三项是可编辑的**。人会在这里把 CA
     * 从 letsencrypt 改成 zerossl、按下发，每一步都成功，而什么也不会发生。
     * 而后端对它们**有校验**，那让它更像真的 —— 一个会拒绝非法值的字段，
     * 读起来就是「系统在认真对待这个输入」。
     *
     * 库里旧 spec 还带着这三个键，后端非严格 Unmarshal 会静默忽略，不用迁移。
     */
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

/**
 * 黑名单。**跟白名单只差一个 `not`**（契约 §6.2）—— 而这张表跟那张表
 * 除了文案几乎一样，正是那件事在界面上的样子。
 *
 * 空名单两者都被后端拒，而**理由相反**，所以两句提示词不同：
 * 空白名单拦下所有人（一次事故），空黑名单谁也拦不到（一条静默失效的规则）。
 * 界面上它们长得一模一样：一条启用着的规则。
 */
export const IP_BLACKLIST_FIELDS = fieldsOf<RuleDraft>([
  ENABLED_FIELD,
  {
    kind: 'area',
    field: 'spec.ips',
    label: '拦截的来源 IP',
    rows: 7,
    hint: (v) =>
      `共 ${normalizeLines((v.spec as { ips?: string[] }).ips).length} 条。名单里的来源按各域名自己的处置方式（abort / 403）拦下，其余放行。`,
    validate: (v) => {
      const ips = normalizeLines((v.spec as { ips?: string[] }).ips)
      /*
       * **空黑名单的提示词跟空白名单相反。**
       *
       * 空白名单是「拦下所有人」——一次事故，人会立刻发现。
       * 空黑名单是「谁也拦不到」——一条**静默失效**的规则，没有任何症状。
       * 两者在列表上长得一模一样：一条启用着的规则。
       */
      if (ips.length === 0) return '名单是空的 —— 这条规则谁也拦不到，而它看起来是启用着的'
      const bad = invalidIps((v.spec as { ips?: string[] }).ips)
      if (bad.length === 0) return null
      return `${bad.length} 行不是合法 IP 或 CIDR：${bad.slice(0, 2).join('、')}`
    },
  },
  APPLY_TO_FIELD,
])

/**
 * 请求特征过滤。**多条之间是「或」**（契约 §6.2）。
 *
 * 那一点写在字段的 hint 里而不是文档里：人配第二条时就该看见「命中任一即拦」，
 * 而不是配完四条之后发现它们不是「且」。想要「且」是配成两条规则 ——
 * 顺带那让每条都能单独开关。
 */
export const REQUEST_FILTER_FIELDS = fieldsOf<RuleDraft>([
  ENABLED_FIELD,
  {
    kind: 'filters',
    field: 'spec.filters',
    label: '请求特征',
    hint: (v) => {
      const n = (v.spec as { filters?: unknown[] }).filters?.length ?? 0
      if (n <= 1) return '命中这条特征的请求会被拦下。'
      return `共 ${n} 条，命中任一即拦 —— 不是「全部满足」。要「且」的话配成两条规则，那也让每条能单独开关。`
    },
    validate: (v) => {
      const fs = (v.spec as { filters?: { field?: string; op?: string; value?: string }[] }).filters ?? []
      if (fs.length === 0) return '一条特征都没有 —— 这条规则谁也拦不到，而它看起来是启用着的'
      const empty = fs.findIndex((f) => !f.value)
      if (empty >= 0) return `第 ${empty + 1} 条没填要匹配的值`
      return null
    },
  },
  APPLY_TO_FIELD,
])

/**
 * 限流。**令牌桶，不是滑动窗口**（契约 §6.2）。
 *
 * `requests` 同时是持续速率的分子和**允许的突发**。说「每 60 秒 100 次，
 * 允许一次性来 100 个」比说「限速」准确 —— 纯速率会误伤正常用户
 * （一个人打开页面会并发十几个请求），**而被误伤过一次的限流，人会直接关掉它，
 * 那时它防的东西一样进得来**。
 *
 * 这张表要说的最要紧的一件事在 `spec.requests` 的 hint 里：**每节点各算各的**。
 */
export function rateLimitFields(nodeCount: number): FieldSpec<RuleDraft>[] {
  return fieldsOf<RuleDraft>([
    ENABLED_FIELD,
    {
      kind: 'text',
      field: 'spec.requests',
      label: '桶容量（次）',
      width: '130px',
      numeric: true,
      /*
       * **「三台节点、每台限 100，全局实际是 300」要在编辑那一刻说。**
       *
       * 一个人按「我要限 100」去配，拿到的是 300 —— 而没有任何地方会告诉他。
       * 保存后再提示是最差的时机：那时他已经认定自己配好了。
       *
       * 这不是能修的：要全局就得有共享计数器，而那给每个请求加一次跨机往返，
       * **在边缘节点上，那个往返比它要防的攻击更容易先把自己拖垮**。
       * 修不了的事情就得说清楚。
       *
       * `nodeCount` 为 0 时不说那句话 —— 那多半是节点列表还没加载，
       * 而「全局约 0」是一句假话。宁可少说一句。
       */
      hint: (v) => {
        const n = Number((v.spec as { requests?: number }).requests ?? 0)
        const base = '桶容量，同时是允许的突发量：一次性来这么多个也放行。'
        if (!nodeCount || !n) return base
        return `${base} 计数每节点各算各的 —— 当前 ${nodeCount} 个节点，全局约 ${n * nodeCount} 次。`
      },
      validate: (v) =>
        isPositiveInt((v.spec as { requests?: number }).requests) ? null : '必须是正整数',
    },
    {
      kind: 'text',
      field: 'spec.window_s',
      label: '补满周期（秒）',
      width: '130px',
      numeric: true,
      hint: (v) => {
        const s = v.spec as { requests?: number; window_s?: number }
        const r = Number(s.requests ?? 0)
        const w = Number(s.window_s ?? 0)
        if (!r || !w) return '每隔这么久把桶补满一次。'
        return `每隔这么久把桶补满一次 —— 持续速率约 ${(r / w).toFixed(1)} 次/秒。`
      },
      validate: (v) =>
        isPositiveInt((v.spec as { window_s?: number }).window_s) ? null : '必须是正整数',
    },
    {
      kind: 'seg',
      field: 'spec.rate_key',
      label: '按什么分桶',
      options: [
        ['ip', '来源 IP'],
        ['ip_path', 'IP + 路径'],
      ],
      /*
       * **`ip_path` 只在配了 request_filter 限定路径时才有意义**（契约 §6.2）。
       *
       * 路径是**攻击者能控制的** —— 不限定路径的话，他每次换一个路径就是一个
       * 新桶，限流等于不存在。而这个开关本身看起来只是「更精细一点」。
       */
      hint: (v) =>
        (v.spec as { rate_key?: string }).rate_key === 'ip_path'
          ? '每个「IP + 路径」一个桶，让猛刷登录接口不影响正常浏览。但路径是攻击者能控的 —— 只在另配了一条请求特征规则限定路径时才有意义，否则他换个路径就是一个新桶。'
          : '每个来源 IP 一个桶。超额回 429 + Retry-After，不是 403。',
    },
    APPLY_TO_FIELD,
  ])
}

/**
 * 地域封禁 / 放行。
 *
 * **两个方向都要显式给，没有默认**（契约 §6.2）—— 与 IP 黑白名单同一条理由：
 * 默认方向相反，猜错一个就是把站点封了或者敞开了。
 */
export const GEO_BLOCK_FIELDS = fieldsOf<RuleDraft>([
  ENABLED_FIELD,
  {
    kind: 'seg',
    field: 'spec.geo_mode',
    label: '方向',
    options: [
      ['block', '拦名单里的'],
      ['allow', '只放名单里的'],
    ],
    /*
     * **「查不到国家」两个方向的行为相反**（契约 §6.2）：
     * block 模式放行，allow 模式拦。
     *
     * 内网地址、保留段在库里查不到国家。一个「只放行中国」的规则不能因为
     * 查不到就把人放进来 —— 这一档是这个选择器真正的分量所在，
     * 而它在两个选项的名字上完全看不出来。
     */
    hint: (v) =>
      (v.spec as { geo_mode?: string }).geo_mode === 'allow'
        ? '只放名单里的国家，其余一律拦。查不到国家的来源也拦 —— 内网地址、保留段查不到。'
        : '拦名单里的国家，其余放行。查不到国家的来源也放行。',
  },
  {
    kind: 'area',
    field: 'spec.geo_countries',
    label: '国家 / 地区',
    rows: 5,
    hint: (v) => {
      const n = normalizeLines((v.spec as { geo_countries?: string[] }).geo_countries).length
      return `每行一个 ISO 3166-1 alpha-2 两位大写代码（CN / US / HK），共 ${n} 个。`
    },
    validate: (v) => {
      const list = normalizeLines((v.spec as { geo_countries?: string[] }).geo_countries)
      if (list.length === 0) return '一个国家都没填 —— 这条规则不会做任何事'
      /*
       * **小写会被后端拒，而它的失败方式是静默的**：mmdb 里存的是大写，
       * 配 `cn` 匹配不到任何东西。所以本地就拦，别等下发。
       *
       * 判据是「两位且全大写」——写全称（`China`）同样落进这一条。
       */
      const bad = list.filter((c) => !/^[A-Z]{2}$/.test(c))
      if (bad.length === 0) return null
      return `${bad.length} 个不是两位大写代码：${bad.slice(0, 3).join('、')} —— 小写在 mmdb 里匹配不到任何东西`
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

/*
 * **分组一起去掉了，因为分组的作用是对比。**
 *
 * 原来分两组：「主控签发参数（不下发给节点）」与「下发到节点的 TLS 配置」——
 * 后者的信息量全部来自前者的存在。主控不再签发，前一组的三项没了，
 * 而单独留下「下发到节点的」这个标题会暗示**还有一类不下发的**，
 * 那类现在不存在。
 *
 * 与顶部 banner 那句「其中 M 张未开启自动续期」同形：一句话字面上仍然成立，
 * 而**它暗示的对比不存在了** —— 这类比直接说错难发现得多。
 */

export const TLS_FIELDS = fieldsOf<TlsPolicy>([
  {
    kind: 'seg',
    field: 'spec.min_version',
    label: '最低 TLS 版本',
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
    onText: '开启。需在防火墙放行 443/udp。',
    offText: '关闭。弱网环境下首包延迟会高于 QUIC。',
  },
  {
    kind: 'switch',
    field: 'spec.hsts',
    label: 'HSTS',
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
    width: '160px',
    numeric: true,
    visible: (v) => v.spec.hsts === true,
  },
  {
    kind: 'switch',
    field: 'spec.ocsp',
    label: 'OCSP Must-Staple',
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
  /*
   * 限流三项：**官方 Caddy 没有限流模块**，所以这个全局开关做不到。
   *
   * 2.11.4 的 132 个标准模块里一个都没有，caddy-ratelimit 是插件；要装它就得
   * 自建 Caddy 二进制，而「节点跑官方包」是 ADR-0001 与 ADR-0003 **共同的**前提。
   *
   * 置灰而不是删掉：删掉的话，看过设计稿的人会以为这一版还没做，过两天再问一次；
   * 置灰 + 就地说清原因，问题当场被回答掉。
   *
   * ## 而现在限流做得到了 —— 走的是另一条路
   *
   * 后端把它做成了一种**访问规则**（`rate_limit`），由 Caddy 通过 forward_auth
   * 委托给节点上的 Agent 判断（ADR-0003 那条委托本来就是为「Caddy 做不到的
   * 准入判断」建的）。契约 §6.3 这个全局开关仍然是 1002，两件事不冲突。
   *
   * **但界面上会冲突**：人在这里看到「做不到」，转头在访问控制里建了一条
   * 工作正常的限流规则。那句话没错，而**一句只说了一半的实话，
   * 读起来跟假话一样**。所以这里要指路 —— 它是这一版唯一会看到这个开关的地方。
   */
  {
    kind: 'switch',
    field: 'spec.rate_limit',
    label: '请求限流',
    onText: '按来源 IP 限速。',
    offText: '不限流。',
    // 已经是 true 时不置灰 —— 那是个下发一定会被拒的状态，必须留一条出路
    unavailable: (v) =>
      v.spec.rate_limit === true
        ? null
        : '官方 Caddy 没有限流模块，这个全局开关做不到。要限流请在访问控制里新建一条「限流」规则 —— 那条由节点上的 Agent 判断，还能按域名分别配。',
    validate: (v) =>
      v.spec.rate_limit === true
        ? '这条会让下发被拒绝：官方 Caddy 没有限流模块。请关掉，改用访问控制里的「限流」规则。'
        : null,
  },
  // 条件字段：契约 §6.3 说 rate_limit=false 时这两个键可能根本不存在，
  // 关闭时不渲染，也不要偷偷填默认值——那会让 diff 里凭空多出两行。
  // 只有库里已经是 true（非法）时才会露面，所以一并置灰：改了也不会生效。
  {
    kind: 'text',
    field: 'spec.rate_rps',
    label: '每秒请求数',
    width: '130px',
    numeric: true,
    visible: (v) => v.spec.rate_limit === true,
    unavailable: () => '这个全局开关做不到，这个值不会生效。限流请用访问控制里的规则。',
  },
  {
    kind: 'text',
    field: 'spec.rate_burst',
    label: '突发上限',
    width: '130px',
    numeric: true,
    visible: (v) => v.spec.rate_limit === true,
    unavailable: () => '这个全局开关做不到，这个值不会生效。限流请用访问控制里的规则。',
  },
])

/**
 * 字段表需要的、**字段值以外**的上下文。
 *
 * 加这个参数是因为有两句话光看草稿说不出来：限流要说「当前 N 个节点，
 * 全局约 N×100」，而 N 不在这条规则里；请求特征的 op 下拉要照后端报的表渲染，
 * 那张表也不在这条规则里。
 *
 * 传成参数而不是让字段表去 import store：字段表是纯的，
 * 而那正是它能被单测逐条证伪的原因。
 */
export interface FieldsContext {
  /** 当前节点数。限流那句「全局约 N×100」用。0 = 还不知道，那句话就不说。 */
  nodeCount?: number
}

/**
 * 按资源 key 与其有效值挑出该用哪张表。
 *
 * ## 未知类型不再兜底成白名单
 *
 * 这里原先是 `return IP_WHITELIST_FIELDS`，对任何认不出的 type 都给白名单那张表。
 * 加进四种新类型的那一刻，**这个兜底会变成一个安静的错**：
 *
 * `ip_blacklist` 的 spec 跟白名单一模一样（都只有一个 `ips`），
 * 于是那张表**正常工作** —— 输入框在、校验在、保存也对，
 * 而每一句文案都在说「允许的来源」，**人正在编辑的是一条拦截规则**。
 *
 * 另外三种更明显（字段对不上，表单是空的），但同样不报错。
 *
 * 现在返回空表：**一张空表在界面上是看得见的**（工作台会显示「这个类型
 * 还没有编辑器」），而一张错的表看起来跟对的一样。
 */
export function fieldsFor(
  resKey: string,
  value: unknown,
  ctx: FieldsContext = {},
): FieldSpec<never>[] {
  const kind = resKey.slice(0, resKey.indexOf(':'))
  if (kind === 'route') return ROUTE_FIELDS as FieldSpec<never>[]
  if (kind === 'rule') {
    const t = (value as RuleWire).type
    if (t === 'ip_whitelist') return IP_WHITELIST_FIELDS as FieldSpec<never>[]
    if (t === 'ip_blacklist') return IP_BLACKLIST_FIELDS as FieldSpec<never>[]
    if (t === 'request_filter') return REQUEST_FILTER_FIELDS as FieldSpec<never>[]
    if (t === 'rate_limit') return rateLimitFields(ctx.nodeCount ?? 0) as FieldSpec<never>[]
    if (t === 'geo_block') return GEO_BLOCK_FIELDS as FieldSpec<never>[]
    if (t === 'service_secret') return SERVICE_SECRET_FIELDS as FieldSpec<never>[]
    if (t === 'jwt_bearer') return JWT_BEARER_FIELDS as FieldSpec<never>[]
    return []
  }
  const id = resKey.slice(resKey.indexOf(':') + 1)
  return (id === 'tls' ? TLS_FIELDS : LOG_FIELDS) as FieldSpec<never>[]
}
