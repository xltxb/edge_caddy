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

/**
 * 契约 §4 `PUT /nodes/:id`：**能改的只有这四项，四项都必填。**
 *
 * **没有 `node_id`** —— 那是这台机器的身份，写在隧道证书的 CN 里（ADR-0009）。
 * 改它等于换一台机器，那是「删掉再接一台」。后端严格绑定，带上它会被 1001 拒
 * **并点名**。这里的类型就是那道护栏：`NodeTokenBody` 有 `node_id` 而这个没有，
 * 两者字段只差一个，**照着上面那个改出来是最容易犯的错**。
 *
 * **也没有 `status` / `dns_enabled` / `drained_at`。** 那些是观察和意图，各有
 * 自己的写入路径（ADR-0014）。一个能改 `status` 的编辑框会让人以为可以手工把
 * 死机器改成在线 —— 而那台机器不会因此活过来。
 */
export interface NodeUpdateBody {
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

/**
 * **平的，跟 `GET /alerts` 的形状不一样** —— 四个字段都可省，省掉 = 保持不变。
 *
 * 这里原先照 `GET` 的形状发（`webhook: {...}` / `lark: { at_all_on_crit }`），
 * 因为契约把两者并成了一个代码块、一份 JSON，读起来就是「PUT 发 GET 那个形状」。
 * 后端的 `ShouldBindJSON` 静默丢掉嵌套的那一层，回 `code: 0` —— 于是
 * **「严重告警 @所有人」这个开关从来没存进去过**，而界面每次都说「已保存」。
 *
 * `notify_level` 恰好两边都在顶层，所以它一直是好的 —— **过的那一半掩护了
 * 没过的那一半**，我验的时候正好验中了能过的那个。
 *
 * PUT 的形状**必然**与 GET 不同：GET 里只有 `url_configured: true/false`，
 * 凭证不回显，没有地方放 webhook 地址。既然必然不同，就让它明显不同：
 * **看着一样而里面字段名不一样，比明显不一样更容易骗到人。**
 */
export interface AlertsPutBody {
  notify_level?: NotifyLevel
  /** 只写入不回显：空 = 保持不变，带了就是替换。 */
  webhook_url?: string
  lark_webhook?: string
  at_all_on_crit?: boolean
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
  /*
   * **没有 `ops_bot_token`。** 后端的 `systemReq` 里没有这个字段，而这个端点是
   * 严格绑定 —— 发过去不是被忽略，是**整个设置保存被拒**。
   *
   * 契约那句「`ops_bot_token_configured` 同理（带了就是替换）」是假的：它只从
   * 环境变量 `EC_OPS_BOT_TOKEN` 读，主控启动时装进鉴权中间件，API 改不了。
   * 而这不是没做完 —— 它是免登录调用主控的凭证，**让一个已登录会话去铸一把
   * 长期钥匙，跟改 CA、改监听地址是同一类事**，属于部署面不属于控制台。
   *
   * 我曾把它登记在这里，而界面上根本没有那个输入框 —— 一条登记表里的假话。
   */
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
  /* 四项都必填，且**没有 node_id** —— 理由在 NodeUpdateBody 的注释里。 */
  'PUT /nodes/:id': shape<NodeUpdateBody>()({
    required: ['city', 'vendor', 'line', 'public_ip'],
    optional: [],
  }),
  'DELETE /nodes/:id': { required: [], optional: [] },

  'POST /routes': shape<RouteCreateBody>()({
    required: ['domain', 'upstream', 'block_mode', 'mtls', 'compress', 'body_max', 'whitelist'],
    optional: [],
  }),
  'PUT /rules/:id': shape<RulePutBody>()({
    required: ['name', 'type', 'enabled', 'apply_to', 'spec'],
    optional: ['secret'],
  }),
  'DELETE /rules/:id': { required: [], optional: [] },
  /* 没有 body；`?force=true` 是查询参数，不进这张表（这里登记的是 body 字段）。 */
  'DELETE /certs/:domain': { required: [], optional: [] },

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
  /*
   * **没有 `DELETE /drafts`。** 后端有这个端点，前端不调它。
   *
   * 它曾经登记在这里，而对应的 `discardAll()` 定义了、导出了、**没有任何
   * 调用方，也没有测试**。于是这份清单在向后端宣称一件不会发生的事 ——
   * 跟 `ops_bot_token` 一样，是登记表里的一句假话。
   *
   * 这一条不是靠巧合发现的（`ops_bot_token` 是），是把 22 条写路径在真主控上
   * 逐个点过来撞出来的。**「登记的都真的会发」这个方向没有任何静态检查守着**：
   * `check-requests` 只比端点在不在源码里出现，出现了就算，不管那段代码有没有
   * 人调。
   *
   * 「放弃全部草稿」这件事本身不是不该有 —— 回滚会把差异写回草稿，那之后
   * 确实可能想整批扔掉。但**没做过的功能不该先在契约层留个影子**：真要做的时候
   * 加回来是四行。
   */

  'POST /deploys/preview': shape<DeployResKeysBody>()({ required: ['res_keys'], optional: [] }),
  'POST /deploys': shape<DeployResKeysBody>()({ required: ['res_keys'], optional: [] }),
  'POST /deploys/:cfg/rollback': { required: [], optional: [] },

  'PUT /dns/weights': shape<DnsWeightsBody>()({ required: ['lines'], optional: [] }),

  /*
   * **没有 `POST /certs/:domain/renew`，也没有 `POST /certs/renew-check`。**
   *
   * 主控不再签发也不再续期证书 —— 证书只从外部平台导入（`PUT /certs/:domain`），
   * 那两个端点后端已经删了。留在这张表里就是在向后端宣称一件不会发生的事，
   * 跟此前 `DELETE /drafts` 那条一样。
   *
   * 而这次是**后端那条断言反过来救的场**：它平时守「前端发了后端收不下的
   * 字段」，这次报的是「后端删了而前端还在调」—— 同一条断言，两个方向。
   */

  'PUT /alerts': shape<AlertsPutBody>()({
    required: [],
    optional: ['notify_level', 'webhook_url', 'lark_webhook', 'at_all_on_crit'],
  }),
  'POST /alerts/test': shape<AlertTestBody>()({ required: ['channel'], optional: [] }),

  'PUT /settings': shape<SettingsPutBody>()({
    required: [],
    optional: [
      'heartbeat_interval_s',
      'offline_threshold_count',
      'auto_drop_dns',
      'dns_provider',
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
}
