/**
 * 前端会往每个写端点发哪些字段。
 *
 * 两件事共用这一份：
 *
 * 1. **前端自己的护栏。** 设置页那个 bug 是 `const body: Record<string, unknown>`
 *    放过去的 —— 我往里塞了 `ops_bot_token_configured`（一个只读状态位），
 *    后端静默丢弃并回 `code: 0`，界面弹「设置已保存」而什么都没存进去。
 *    一个 `Record<string, unknown>` 的 body 等于没有类型。
 *
 * 2. **导给后端做「后端接受的字段集 ⊇ 前端会发的字段集」。** 后端现在
 *    `DisallowUnknownFields`，前端发一个它没有的 key 就是 1001 —— 而那件事
 *    今天只在运行时才知道，那条路径没人点就要等到发布。
 *
 * ## 为什么这份东西不会过期
 *
 * 字段清单**不是手写的第二份知识**：`shape<T>()` 要求列出的名字与 `T` 的键
 * **完全一致**，多一个、少一个都编译不过（少的那个还会在报错信息里点名）。
 * 于是「改了接口忘了改清单」不存在 —— 它是同一份知识的两种形态，由类型检查器
 * 钉在一起。
 *
 * `scripts/check-requests.mjs` 再钉另一头：`src/` 里每一个带 body 的
 * `http.post` / `http.put` 调用，其路径都必须在这里登记过。新加端点忘了登记会红。
 *
 * ## 它验不了什么
 *
 * **只验字段名，不验语义。**`{"kind":"dnspod"}` 和 `{"kind":"随便什么"}` 在这里
 * 一样。取值对不对仍然只有真跑一遍能知道 —— 这条边界要说在前面，免得这份东西
 * 看起来比实际牢。
 */

import type { BlockMode, DnsProviderFields, DnsProviderPatch, NotifyLevel } from './types'

/* ── 让类型和字段清单互相钉住的机关 ─────────────────────────────────── */

type RequiredKeys<T> = { [K in keyof T]-?: object extends Pick<T, K> ? never : K }[keyof T]
type OptionalKeys<T> = { [K in keyof T]-?: object extends Pick<T, K> ? K : never }[keyof T]

/**
 * 声明一个端点的字段清单，**与它的类型逐字对齐**。
 *
 * 少写一个键：报错是「缺少属性 `这个清单漏了`，其类型为 `'auto_drop_dns'`」——
 * **漏掉的那个名字就在报错里**。多写一个：它不在键的联合里，同样过不去。
 */
function shape<T extends object>() {
  return <R extends readonly RequiredKeys<T>[], O extends readonly OptionalKeys<T>[]>(spec: {
    required: R &
      ([Exclude<RequiredKeys<T>, R[number]>] extends [never]
        ? unknown
        : { 这个清单漏了: Exclude<RequiredKeys<T>, R[number]> })
    optional: O &
      ([Exclude<OptionalKeys<T>, O[number]>] extends [never]
        ? unknown
        : { 这个清单漏了: Exclude<OptionalKeys<T>, O[number]> })
  }): Shape => ({
    required: spec.required as readonly string[],
    optional: spec.optional as readonly string[],
  })
}

export interface Shape {
  readonly required: readonly string[]
  readonly optional: readonly string[]
  /** 字段名由别处决定（资源的字段表），见各处注释。 */
  readonly dynamic?: string
}

/* ── 各端点的 body ──────────────────────────────────────────────────── */

export interface LoginBody {
  username: string
  password: string
}

/** 契约 §4：下线必须显式确认，后端会拒绝没带 `confirm` 的请求。 */
export interface NodeDrainBody {
  confirm: boolean
}

export interface NodeDnsBody {
  enabled: boolean
}

export interface NodeTokenBody {
  node_id: string
  city: string
  vendor: string
  line: string
  public_ip: string
}

export interface RouteCreateBody {
  domain: string
  upstream: string
  block_mode: BlockMode
  mtls: boolean
  compress: boolean
  body_max: string
  whitelist: string[]
}

/**
 * 契约 §6.4：共享密钥走**顶层 `secret`**，不进 `spec`，也不进草稿。
 * 草稿是全局可见的（`GET /drafts`），把密钥放进去等于广播它。
 */
export interface RulePutBody {
  name: string
  type: string
  enabled: boolean
  apply_to: string[]
  spec: Record<string, unknown>
  secret?: string
}

export interface DeployResKeysBody {
  res_keys: string[]
}

export interface AlertsPutBody {
  notify_level: NotifyLevel
  webhook: { url_configured: boolean }
  lark: { webhook_configured: boolean; at_all_on_crit: boolean }
  /** 只写入不回显：填了就是替换，不填就是保持不变。 */
  webhook_url?: string
  lark_webhook?: string
}

export interface AlertTestBody {
  channel: string
}

export interface DnsWeightsBody {
  lines: { code: string; entries: { node: string; weight: number }[] }[]
}

/**
 * 契约 §11：`master_endpoint` 只读（`PUT` 带它一律 1002），
 * `ops_bot_token_configured` / `warn_*` 是只读状态位，都不在这里。
 *
 * **这份接口就是那个白名单本身** —— 原先是 `Record<string, unknown>` 加人工
 * 排除，那是个黑名单，要随后端加字段一起维护，漏一个就发出去一个。
 */
export interface SettingsPutBody {
  heartbeat_interval_s?: number
  offline_threshold_count?: number
  auto_drop_dns?: boolean
  dns_provider?: DnsProviderPatch
  ops_bot_token?: string
}

/**
 * **派生自 `types.ts` 的 `DnsProviderFields`，不另写一份。**
 *
 * 同一份知识写两遍就是我在这个文件里想消灭的东西 —— 两处定义迟早对不上，
 * 而对不上的那一刻没有任何东西会红。`clear` 去掉：它在类型里是
 * `clear?: never`（用来让联合类型拦住「clear 与其他字段同给」），
 * 不是一个前端会发的字段名。
 */
export type DnsProviderSubBody = Omit<DnsProviderFields, 'clear'>

/* ── 登记表 ─────────────────────────────────────────────────────────── */

/**
 * 端点 → 字段清单。键的写法是 `METHOD /path`，路径里的变量写成 `:x`。
 *
 * 没有 body 的写端点（`POST /nodes/:id/push`、`POST /auth/logout`、
 * `DELETE /drafts` 这些）**也要登记**，写成两个空数组 —— 否则
 * `check-requests` 分不清「没有 body」和「忘了登记」。
 */
export const REQUEST_SHAPES: Record<string, Shape> = {
  'POST /auth/login': shape<LoginBody>()({ required: ['username', 'password'], optional: [] }),
  'POST /auth/logout': { required: [], optional: [] },

  'POST /nodes/:id/push': { required: [], optional: [] },
  'POST /nodes/:id/rejoin': { required: [], optional: [] },
  'POST /nodes/:id/probe': { required: [], optional: [] },
  'POST /nodes/:id/dns': shape<NodeDnsBody>()({ required: ['enabled'], optional: [] }),
  'POST /nodes/:id/drain': shape<NodeDrainBody>()({ required: ['confirm'], optional: [] }),
  'POST /nodes/token': shape<NodeTokenBody>()({
    required: ['node_id', 'city', 'vendor', 'line', 'public_ip'],
    optional: [],
  }),

  'POST /routes': shape<RouteCreateBody>()({
    required: ['domain', 'upstream', 'block_mode', 'mtls', 'compress', 'body_max', 'whitelist'],
    optional: [],
  }),
  'PUT /rules/:id': shape<RulePutBody>()({
    required: ['name', 'type', 'enabled', 'apply_to', 'spec'],
    optional: ['secret'],
  }),
  'DELETE /rules/:id': { required: [], optional: [] },

  /*
   * 草稿的字段名**由资源的字段表决定**，不是一个固定集合：一条草稿是某个资源
   * spec 的 Partial，键取自 `src/workbench/fields.ts` 那张表，而那张表按资源
   * 种类（route / rule / policy）不同。
   *
   * 所以这里不列名字，只标出它从哪来 —— **写「没有字段」会是假话**，
   * 而假话比空白更糟：后端读到空清单会以为这个端点前端什么都不发。
   */
  'PUT /drafts/:key': {
    required: [],
    optional: [],
    dynamic: '资源 spec 的 Partial，键取自 src/workbench/fields.ts 的字段表',
  },
  'DELETE /drafts': { required: [], optional: [] },

  'POST /deploys/preview': shape<DeployResKeysBody>()({ required: ['res_keys'], optional: [] }),
  'POST /deploys': shape<DeployResKeysBody>()({ required: ['res_keys'], optional: [] }),
  'POST /deploys/:cfg/rollback': { required: [], optional: [] },

  'PUT /dns/weights': shape<DnsWeightsBody>()({ required: ['lines'], optional: [] }),

  'POST /certs/:domain/renew': { required: [], optional: [] },
  'POST /certs/renew-check': { required: [], optional: [] },

  'PUT /alerts': shape<AlertsPutBody>()({
    required: ['notify_level', 'webhook', 'lark'],
    optional: ['webhook_url', 'lark_webhook'],
  }),
  'POST /alerts/test': shape<AlertTestBody>()({ required: ['channel'], optional: [] }),

  'PUT /settings': shape<SettingsPutBody>()({
    required: [],
    optional: [
      'heartbeat_interval_s',
      'offline_threshold_count',
      'auto_drop_dns',
      'dns_provider',
      'ops_bot_token',
    ],
  }),
}

/** 嵌套对象的字段清单，单独登记 —— 后端那条断言要逐层比。 */
export const NESTED_SHAPES: Record<string, Shape> = {
  'PUT /settings#dns_provider': shape<DnsProviderSubBody>()({
    required: [],
    optional: ['kind', 'domain', 'sub', 'credential_mode', 'zone_id', 'account_id', 'email', 'credential'],
  }),
  /* clear 是独立动作，不能与上面任何字段同给 —— 同给后端返回 1002（契约 §11）。 */
  'PUT /settings#dns_provider.clear': { required: ['clear'], optional: [] },
  'PUT /dns/weights#lines[]': shape<DnsWeightsBody['lines'][number]>()({
    required: ['code', 'entries'],
    optional: [],
  }),
  'PUT /dns/weights#lines[].entries[]': shape<DnsWeightsBody['lines'][number]['entries'][number]>()({
    required: ['node', 'weight'],
    optional: [],
  }),
  'PUT /alerts#webhook': shape<AlertsPutBody['webhook']>()({
    required: ['url_configured'],
    optional: [],
  }),
  'PUT /alerts#lark': shape<AlertsPutBody['lark']>()({
    required: ['webhook_configured', 'at_all_on_crit'],
    optional: [],
  }),
}
