/**
 * 领域对象 —— 界面直接消费的形状。
 *
 * 与 `api/types.ts` 的线格式一一对应，但这里是 camelCase，并且把线上的
 * 不确定性收干净：`cpu_series: null` 变成空数组，`node: null` 变成占位符。
 * 转换只发生在这个文件里，组件永远看不到 snake_case，也永远不用判 null。
 */

import type {
  EventKind,
  EventWire,
  NodeStatus,
  NodeWire,
  OverviewKpiWire,
} from './api/types'

export interface EdgeNode {
  id: string
  city: string
  vendor: string
  /**
   * **中转线路**——机房卖的那条网络产品（`CN2 GIA` / `CMIN2`），说的是这台机器
   * 怎么出网。自由文本，接入时人填。
   *
   * **与 DNS 的解析线路（`ct/cu/cm/tw/ov`）没有函数关系，不要试图互推。**
   * 一台 CN2 GIA 的机器同时服务电信、联通、移动的访问者是常态；「哪台机器接
   * 哪条解析线路的流量」是人下的调度决定（CONTEXT.md「中转线路」/「解析线路」，
   * 契约 §8）。两边字段都叫 `line`，而**同名会制造一个不存在的承诺**——
   * 我就是因此问出「映射在哪一步做」的。
   */
  line: string
  ip: string
  status: NodeStatus
  /**
   * 隧道此刻连着吗（实时，无去抖）。**徽标不认它，认 status。**
   *
   * 留着是为了让它与 status 的不一致**能被人看见**：契约 §4 明写短暂不一致
   * 正常（那是判定的去抖窗口）、持续不一致是 bug。2026-08-23 那次就是持续
   * 不一致，而界面上没有任何一处能看出来。
   *
   * **前端不判定「这是不是 bug」** —— 那要拿去抖窗口（`heartbeat_interval_s ×
   * offline_threshold_count`）当阈值，而这一页没有那两个值，只有设置页有。
   * 在这里写死一个秒数就是第二份知识：设置改了它不会跟着改，也不会有任何东西
   * 变红。所以详情里只把两个事实并列摆出来，判断留给人。
   */
  online: boolean
  /**
   * 过去一小时**主控这边记录到几次隧道建立**。**null = 数不出来，不是 0。**
   *
   * 不是「断连几次」：那是那台机器自己的说法，而主控这份是会话表里的一手观测。
   * 措辞的理由见 `@/nodes/flags` 的 `reconnectNote`（契约 §4）。
   *
   * 它是唯一一个能在徽标全绿时指出问题的字段 —— 隧道断开到重连只要 1–2 秒，
   * 而离线判定要 9 秒才翻 down，所以一条每十分钟断一次的隧道，在 status /
   * online / hbAgeMs 上全部是健康的。判断留给人，这里只把数摆出来。
   */
  reconnects1h: number | null
  /**
   * GeoIP 库跟主控那份一致吗（契约 §6.2）。**三档，`null` 不是 `false`。**
   *
   * `null` = 主控自己没有库，即这套系统没在用地域功能 —— 那时**什么都别显示**。
   * 跟上面 `reconnects1h` 的 `null` 正好相反：那个是「数不出来，要说出来」，
   * 这个是「不适用，说了就是天天报假警」。
   *
   * 所以这里**不能 `?? false`**：主控太旧没有这个字段时会被当成「库缺失」，
   * 每台节点标一句红，而这套系统压根没在用地域规则。
   */
  geoDbOk: boolean | null
  cpu: number
  mem: number
  conns: number
  /** 12 点 CPU 百分比，最新在末尾。线上为 null 时是空数组，界面留白而不是报错。 */
  cpuSeries: number[]
  /** 服务端算出的心跳年龄（毫秒）。 */
  hbAgeMs: number
  /**
   * 收到 hbAgeMs 那一刻的本地时间戳。显示年龄 = hbAgeMs + (now - hbStampedAt)。
   * 不用浏览器时钟减 last_hb_at —— 那会被主控与浏览器之间的时钟偏差污染。
   */
  hbStampedAt: number
  cfgVersion: string
  /** Agent 版本；空串 = 还没接入过。灰度部署时人最先问的就是这个。 */
  agentVersion: string
  /** cfg_version ≠ 基线。只比版本号，发现不了 SSH 手改（ADR-0002）。 */
  drift: boolean
  dnsEnabled: boolean
  /**
   * 被下线的时刻，没下线过是 null。**与 status 各记各的**：
   * status 是观察，这个是意图（CONTEXT.md）。
   */
  drainedAt: string | null
  /** 解析是谁关的；空串 = 从没人动过。三种要人做的事不同。 */
  dnsReason: NodeWire['dns_reason']
  /** 操作人；系统自动摘除时是 null，不是 'system'。 */
  dnsActor: string | null
  dnsChangedAt: string | null
  /** 该节点**当前生效配置**里的数量，漂移节点会显示旧值。 */
  routes: number
  rules: number
}

export function fromNodeWire(w: NodeWire, stampedAt = Date.now()): EdgeNode {
  return {
    id: w.id,
    city: w.city,
    vendor: w.vendor,
    line: w.line,
    ip: w.public_ip,
    status: w.status,
    online: w.online ?? false,
    /*
     * `?? null` 同时兜住两件事：后端算不出来、以及主控太旧还没有这个字段。
     * 对看界面的人来说两者一样 —— 这个数拿不到。**不能兜成 0**：0 是
     * 「这条隧道很稳」，而那正是数不出来时最不该说的一句话。
     */
    reconnects1h: w.reconnects_1h ?? null,
    /*
     * `?? null` 兜的是「主控太旧，没有这个字段」，而它落在**不显示**那一档 ——
     * 与上一行同样写法、相反理由：那一行不能兜成 0（0 是「很稳」），
     * 这一行不能兜成 false（false 是「库缺失」）。两个都会让界面说一件没发生的事。
     */
    geoDbOk: w.geo_db_ok ?? null,
    cpu: w.cpu,
    mem: w.mem,
    conns: w.conns,
    cpuSeries: w.cpu_series ?? [],
    hbAgeMs: w.hb_age_ms,
    hbStampedAt: stampedAt,
    cfgVersion: w.cfg_version,
    agentVersion: w.agent_version ?? '',
    drift: w.drift,
    dnsEnabled: w.dns_enabled,
    drainedAt: w.drained_at ?? null,
    dnsReason: w.dns_reason ?? '',
    dnsActor: w.dns_actor ?? null,
    dnsChangedAt: w.dns_changed_at ?? null,
    routes: w.routes,
    rules: w.rules,
  }
}

/** 心跳年龄（秒），随传入的 now 走。 */
export function hbAgeSec(n: EdgeNode, now: number): number {
  return (n.hbAgeMs + Math.max(0, now - n.hbStampedAt)) / 1000
}

export interface ConsoleEvent {
  id: number
  at: string
  /** 系统级事件没有节点，这里统一成破折号，界面不用再判。 */
  node: string
  kind: EventKind
  msg: string
}

/**
 * 「没有值」在线上有两种写法。
 *
 * 契约 §0.4 说该用 `null`，但实际会收到空串。两者都当作「没有」——
 * 只判 null 的话，空串会原样渲染成一个空位，看起来像界面漏了内容。
 * 对上游的写法宽容，对自己的输出严格。
 */
/**
 * Go 的零值时间。后端在「从没发生过」时给这个，而不是 null 或空串。
 *
 * 不挡的话，`0001-01-01T00:00:00Z` 会被格式化成一个像模像样的 `00:00:00`——
 * 一个**格式正确但意思是假的**值，比一个空白危险得多。
 */
export function isZeroTime(iso: string | null | undefined): boolean {
  return !iso || iso.startsWith('0001-01-01')
}

export function orDash(v: string | null | undefined): string {
  return v === null || v === undefined || v === '' ? '—' : v
}

export function fromEventWire(w: EventWire): ConsoleEvent {
  return { id: w.id, at: w.at, node: orDash(w.node), kind: w.kind, msg: w.msg }
}

export interface OverviewKpi {
  /** 三档由后端一条语句产出，前端**不再自行推导**。 */
  nodesOnline: number
  nodesWarn: number
  nodesDown: number
  nodesTotal: number
  connsTotal: number
  /** 较昨日同时段的变化百分比。null = 历史不足，界面留白而不是显示 0%。 */
  connsDeltaPct: number | null
  /** 为什么没有同比数字。三种对人的意思不同，界面各说各的。 */
  connsDeltaReason: OverviewKpiWire['conns_delta_reason']
  /** 回源率。null = 还没有流量样本，算不出来 —— 不要当成 0。 */
  originRate: number | null
  driftNodes: number
}

export function fromKpiWire(w: OverviewKpiWire): OverviewKpi {
  return {
    nodesOnline: w.nodes_online,
    nodesWarn: w.nodes_warn,
    nodesDown: w.nodes_down,
    nodesTotal: w.nodes_total,
    connsTotal: w.conns_total,
    connsDeltaPct: w.conns_delta_pct,
    connsDeltaReason: w.conns_delta_reason ?? null,
    originRate: w.origin_rate,
    driftNodes: w.drift_nodes,
  }
}
