/**
 * 访问规则列表上那两列 —— 「要点」和「状态」，抽成纯函数。
 *
 * 抽出来不是为了复用（只有一处用），是为了**能被证伪**：这两列每一格都是
 * 一句对现实的断言（「只放这 N 条」「这条规则在 3 台节点上不生效」），
 * 而写在模板里的断言，只有人盯着截图看的时候才会被检查一次。
 *
 * 跟 `@/nodes/flags` 同一条路子，理由见那里。
 */

import type { RuleWire } from '@/api/types'

export const TYPE_LABEL: Record<string, string> = {
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
export function ruleSummary(r: RuleWire): string {
  const s = r.spec as Record<string, unknown>
  const n = (k: string) => (s[k] as unknown[] | undefined)?.length ?? 0

  if (r.type === 'ip_whitelist') return `只放这 ${n('ips')} 条来源`
  if (r.type === 'ip_blacklist') return `拦这 ${n('ips')} 条来源`
  if (r.type === 'request_filter') return `${n('filters')} 个特征，命中任一即拦`
  if (r.type === 'rate_limit') {
    const key = s.rate_key === 'ip_path' ? 'IP + 路径' : '按 IP'
    return `每 ${Number(s.window_s ?? 0)} 秒 ${Number(s.requests ?? 0)} 次 · ${key}`
  }
  if (r.type === 'geo_block') {
    const list = (s.geo_countries as string[] | undefined) ?? []
    const head = s.geo_mode === 'allow' ? '只放' : '拦'
    // 国家多时截断 —— 一列摘要放不下二十个，而前几个足以认出这条规则
    const shown = list.slice(0, 6).join('、')
    return `${head} ${shown}${list.length > 6 ? ` 等 ${list.length} 个` : ''}`
  }
  if (r.type === 'service_secret') return `${String(s.header ?? '')} · ${String(s.algo ?? '')}`
  return String(s.iss ?? '')
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
