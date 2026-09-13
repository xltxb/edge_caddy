/**
 * 访问规则列表上那两列 —— 「要点」和「状态」，抽成纯函数。
 *
 * 抽出来不是为了复用（只有一处用），是为了**能被证伪**：这两列每一格都是
 * 一句对现实的断言（「只放这 N 条」「这条规则在 3 台节点上不生效」），
 * 而写在模板里的断言，只有人盯着截图看的时候才会被检查一次。
 *
 * 跟 `@/nodes/flags` 同一条路子，理由见那里。
 */

import type { RuleIssue, RuleWire, RuleType } from '@/api/types'

/**
 * 类型 → 中文标签。
 *
 * **键类型必须是 `RuleType` 而不是 `string`。** `dispatch.test.ts` 拿这张表当
 * 「有哪几种规则」的权威清单去遍历，而 `Record<string, …>` 允许漏掉一种类型
 * 也允许多出一个不存在的键——那时清单本身是错的，遍历得再全也证明不了什么
 * （issue #68）。收窄之后，新增一种 RuleType 而忘了给标签，tsc 当场就报。
 */
export const TYPE_LABEL: Record<RuleType, string> = {
  ip_whitelist: 'IP 白名单',
  ip_blacklist: 'IP 黑名单',
  request_filter: '请求特征',
  rate_limit: '限流',
  geo_block: '地域',
  service_secret: '服务密钥',
  jwt_bearer: 'JWT Bearer',
}

/**
 * 「要点」那一列。**每种类型说出它最容易被看错的那件事。**
 *
 * ## 黑白名单的摘要必须带方向
 *
 * 两者的 spec 一模一样（都只有一个 `ips`），区别全在 `type` 上 ——
 * 而它们在渲染出的 Caddy 配置里只差一个 `not`（契约 §6.2）。
 *
 * 摘要都写「N 条来源」的话，这一列就成了两种**相反**规则的共同外观，
 * 而类型列那两个标签只差一个字。所以写成「只放这 N 条」/「拦这 N 条」：
 * 看一眼就知道方向。
 *
 * ## 限流不写「限速」
 *
 * 它是令牌桶：`requests` 同时是持续速率的分子和**允许的突发**。
 * 「限速」两个字盖住了后一半，而那一半正是它不误伤正常用户的原因 ——
 * 一个人打开页面会并发十几个请求，**而被误伤过一次的限流，人会直接关掉它**。
 *
 * ## 请求特征要说出「或」
 *
 * 多条特征之间是**或**，不是且。列表这一层只能提一句，说清是编辑器的事 ——
 * 但完全不提的话，人会按「且」去读这个数字。
 */
/**
 * 每种类型的要点。**写成 `Record<RuleType, …>` 而不是 if 级联**：
 *
 * 级联的最后一支是兜底，于是加第八种类型时它会**静默地**拿到前一种的措辞，
 * 或者像 jwt_bearer 那样落进一个 `String(s.iss ?? '')`——iss 没配时就是空串，
 * 那一列在界面上一片空白，而没有任何东西会红（issue #68）。
 *
 * Record 少一个键编译器就红。加类型的人因此不必知道这张表存在。
 */
const SUMMARY: Record<RuleType, (s: Record<string, unknown>, n: (k: string) => number) => string> = {
  ip_whitelist: (_s, n) => `只放这 ${n('ips')} 条来源`,
  ip_blacklist: (_s, n) => `拦这 ${n('ips')} 条来源`,
  request_filter: (_s, n) => `${n('filters')} 个特征，命中任一即拦`,
  rate_limit: (s) => {
    const key = s.rate_key === 'ip_path' ? 'IP + 路径' : '按 IP'
    return `每 ${Number(s.window_s ?? 0)} 秒 ${Number(s.requests ?? 0)} 次 · ${key}`
  },
  geo_block: (s) => {
    const list = (s.geo_countries as string[] | undefined) ?? []
    const head = s.geo_mode === 'allow' ? '只放' : '拦'
    // 国家多时截断 —— 一列摘要放不下二十个，而前几个足以认出这条规则
    const shown = list.slice(0, 6).join('、')
    return `${head} ${shown}${list.length > 6 ? ` 等 ${list.length} 个` : ''}`
  },
  service_secret: (s) => `${String(s.header ?? '')} · ${String(s.algo ?? '')}`,
  // **iss 没配时也要说点什么。** 空串会让这一列一片空白，
  // 而人分不出「这条规则没有要点」和「这一列坏了」。
  jwt_bearer: (s) => {
    const iss = String(s.iss ?? '')
    return iss === '' ? '尚未填签发者（iss）' : `签发者 ${iss}`
  },
}

export function ruleSummary(r: RuleWire): string {
  const s = r.spec as Record<string, unknown>
  const n = (k: string) => (s[k] as unknown[] | undefined)?.length ?? 0
  const f = SUMMARY[r.type]
  // 主控报了一种控制台还不认识的类型（issue #67 同一条）：说出来，不装作认识。
  if (!f) return `控制台还不认识这种类型（${String(r.type)}）`
  return f(s, n)
}

export interface RuleStatus {
  text: string
  tone: 'ok' | 'warn'
}

/**
 * 状态列说的是**这条规则此刻做不做事**，不是那个开关的位置。
 *
 * 只看 `enabled` 的话，一条启用了但没绑任何域名的规则会显示「生效中」，
 * 而右边的「应用到」同时说它不生效 —— 两列各说各话，人只会信左边那个绿的。
 *
 * 「不做事」的几种原因都收在这里，而不是各开一列：**多一列就多一次自相矛盾
 * 的机会**。
 *
 * @param staleGeoNodes 缺 / 旧 GeoIP 库的节点数。见下面地域那一档。
 */
export function ruleStatus(r: RuleWire, staleGeoNodes: number): RuleStatus {
  if (!r.enabled) return { text: '已停用', tone: 'warn' }
  if (!r.apply_to.length) return { text: '已启用，未生效', tone: 'warn' }
  /*
   * 密钥那一条在真主控上进不来（`PUT /rules/:id` 不给密钥就被 1002 拒），
   * 但这个布尔是后端真发的字段 —— 它要是 false，说「生效中」就是撒谎。
   */
  if (r.type === 'service_secret' && r.spec.secret_configured === false) {
    return { text: '未设置密钥', tone: 'warn' }
  }
  /*
   * **一条地域规则在缺库的节点上形同虚设**（契约 §6.2）。
   *
   * 后端那条决定是「库还没到时放行，而不是全封」—— 理由是把我们的问题变成
   * 所有访问者的 403，是把一次运维疏忽放大成一次全站故障。
   * 代价说在明处：**那段时间里这条规则什么也不拦**。
   *
   * 契约要求它必须被看见。节点页已经标了「哪台机器缺库」，
   * 而那一句说不出「哪条规则因此失效」—— 人得自己把两页对起来。
   *
   * ## 这一档的限度
   *
   * `staleGeoNodes` 为 0 有两个意思：**真的都齐了，和调用方还不知道**
   * （节点列表没加载 / 加载失败）。这里分不出来，也不该在这里分 ——
   * 那是调用方的事，它得先去把节点拉回来。
   *
   * 所以这个函数的契约是：**传 0 就意味着「你确认没有缺库的节点」**。
   * 拿不到数据时传 0，得到的是一句「生效中」，而那可能是谎。
   */
  if (r.type === 'geo_block' && staleGeoNodes > 0) {
    return { text: `${staleGeoNodes} 台节点上不生效`, tone: 'warn' }
  }
  return { text: '生效中', tone: 'ok' }
}

/** 一条规则的「完整性」在界面上该怎么表现。 */
export interface Completeness {
  /** 要不要标出来。 */
  flag: boolean
  /** 标签文案。 */
  text: string
  /** 原样列出的原因 —— 不改写、不截断。它说的是这一条为什么不完整。 */
  reasons: string[]
}

/**
 * 这条规则还差什么（契约 §6.2）。
 *
 * ## 三档，而只有两档该说话
 *
 * | `issues` | 意思 | 结果 |
 * |---|---|---|
 * | 非空数组 | 差这几样，下发会被拒 | 标出来 + 原样列 reason |
 * | `[]` | 算过了，它是完整的 | 不显示 |
 * | `null` | **这一条这次算不出来** | 标出来，但说的是「说不了」 |
 *
 * ## 整张表缺失是第四档，而它由调用方处理
 *
 * 主控太旧时 `GET /rules` 根本没有 `incomplete` —— 那时**这个功能不可用**，
 * 界面上一条都不该标。跟 `geo_db_ok` 的 `null` 同一条：一套没有这个能力的
 * 系统，每条规则都挂一个「说不了」，是在报告一个不存在的问题，
 * 而人两天就学会忽略它。
 *
 * 所以这个函数只在**表在**的时候被调用，`undefined` 与 `null` 的区别
 * 留给调用方 —— 这里把 `undefined` 当成「表在而这条没被算到」，
 * 那与 `null` 是同一件事。
 *
 * ## 不过滤 `apply_to` 那条
 *
 * 「未绑定域名」在列表上已经有一个标签了，看起来重复。**但不去重**：
 * 这份清单与下发那次的校验共用同一段代码，界面把它原样呈现 ——
 * 前端自己挑掉一条，就等于在这里另做了一份判断，而那份判断迟早跟后端分叉。
 */
export function completenessOf(issues: RuleIssue[] | null | undefined): Completeness {
  if (issues === null || issues === undefined) {
    return { flag: true, text: '完整性说不了', reasons: [] }
  }
  if (issues.length === 0) return { flag: false, text: '', reasons: [] }
  return {
    flag: true,
    text: '还不完整',
    reasons: issues.map((i) => i.reason),
  }
}
