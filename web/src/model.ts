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
  /** 系统级事件在线上是 null，这里统一成破折号，界面不用再判。 */
  node: string
  kind: EventKind
  msg: string
}

export function fromEventWire(w: EventWire): ConsoleEvent {
  return { id: w.id, at: w.at, node: w.node ?? '—', kind: w.kind, msg: w.msg }
}

export interface OverviewKpi {
  nodesOnline: number
  nodesTotal: number
  connsTotal: number
  /** 较昨日同时段的变化百分比。null = 历史不足，界面留白而不是显示 0%。 */
  connsDeltaPct: number | null
  /** 回源率 = 到达 upstream ÷ 边缘收到的总请求。越低越好（低 = 边缘挡掉得多）。 */
  originRate: number
  driftNodes: number
}

export function fromKpiWire(w: OverviewKpiWire): OverviewKpi {
  return {
    nodesOnline: w.nodes_online,
    nodesTotal: w.nodes_total,
    connsTotal: w.conns_total,
    connsDeltaPct: w.conns_delta_pct,
    originRate: w.origin_rate,
    driftNodes: w.drift_nodes,
  }
}
