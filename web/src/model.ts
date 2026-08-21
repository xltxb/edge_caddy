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
  line: string
  ip: string
  status: NodeStatus
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
  /** cfg_version ≠ 基线。只比版本号，发现不了 SSH 手改（ADR-0002）。 */
  drift: boolean
  dnsEnabled: boolean
  /**
   * 被下线的时刻，没下线过是 null。**与 status 各记各的**：
   * status 是观察，这个是意图（CONTEXT.md）。
   */
  drainedAt: string | null
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
    cpu: w.cpu,
    mem: w.mem,
    conns: w.conns,
    cpuSeries: w.cpu_series ?? [],
    hbAgeMs: w.hb_age_ms,
    hbStampedAt: stampedAt,
    cfgVersion: w.cfg_version,
    drift: w.drift,
    dnsEnabled: w.dns_enabled,
    drainedAt: w.drained_at ?? null,
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
    originRate: w.origin_rate,
    driftNodes: w.drift_nodes,
  }
}
