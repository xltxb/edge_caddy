import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { http } from '@/api/http'
import type {
  DnsToggleWire,
  DrainWire,
  HeartbeatFrame,
  LogLevel,
  NodeTokenWire,
  NodeWire,
  Paged,
  ProbeWire,
} from '@/api/types'
import { fromNodeWire, type EdgeNode } from '@/model'

/** sparkline 固定 12 点，追加时从头部挤掉最旧的。 */
const SERIES_LEN = 12

export const useNodesStore = defineStore('nodes', () => {
  const items = ref<EdgeNode[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)
  /** 各节点正在进行的异步操作，用于按钮 loading 态。 */
  const busy = ref<Record<string, string>>({})
  /** 行展开后加载的 Agent 日志，按节点缓存。 */
  const logs = ref<Record<string, { at: string; level: LogLevel; msg: string }[]>>({})
  /** 最近一次探活结果。Caddy Admin 与隧道分开报 —— 两种故障处置不同。 */
  const probes = ref<Record<string, ProbeWire>>({})

  const byId = computed(() => new Map(items.value.map((n) => [n.id, n])))
  const online = computed(() => items.value.filter((n) => n.status !== 'down'))
  const drifted = computed(() => items.value.filter((n) => n.drift))

  async function fetchAll(): Promise<void> {
    loading.value = true
    error.value = null
    try {
      const page = await http.get<Paged<NodeWire>>('/nodes')
      items.value = page.items.map((w) => fromNodeWire(w))
    } catch (e) {
      error.value = e instanceof Error ? e.message : '加载节点失败'
      throw e
    } finally {
      loading.value = false
    }
  }

  /** WS heartbeat 帧就地更新。找不到的节点直接忽略——列表由 REST 决定成员。 */
  function applyHeartbeat(frame: HeartbeatFrame): void {
    const d = frame.data
    const n = items.value.find((x) => x.id === d.id)
    if (!n) return

    n.status = d.status
    n.cpu = d.cpu
    n.mem = d.mem
    n.conns = d.conns
    n.hbAgeMs = d.hb_age_ms
    n.hbStampedAt = Date.now()
    n.cfgVersion = d.cfg_version
    n.routes = d.routes
    n.rules = d.rules
    // drift 由基线决定，不在心跳帧里；由 overview store 的基线变化触发重算。

    const next = [...n.cpuSeries, Math.round(d.cpu)]
    n.cpuSeries = next.slice(-SERIES_LEN)
  }

  /** 基线变了要重算全体 drift —— 版本号比对是漂移的**唯一**依据（ADR-0002）。 */
  function recomputeDrift(baseline: string): void {
    for (const n of items.value) n.drift = n.cfgVersion !== baseline
  }

  async function withBusy<T>(id: string, label: string, fn: () => Promise<T>): Promise<T> {
    busy.value = { ...busy.value, [id]: label }
    try {
      return await fn()
    } finally {
      const copy = { ...busy.value }
      delete copy[id]
      busy.value = copy
    }
  }

  async function fetchLogs(id: string): Promise<void> {
    const page = await http.get<Paged<{ at: string; level: LogLevel; msg: string }>>(
      `/nodes/${encodeURIComponent(id)}/logs`,
    )
    logs.value = { ...logs.value, [id]: page.items }
  }

  /** 把当前基线重推给单个节点。对已下线节点后端返回 2001。 */
  function pushOne(id: string): Promise<{ deploy_id: number; cfg_version: string }> {
    return withBusy(id, '重推中', () =>
      http.post<{ deploy_id: number; cfg_version: string }>(`/nodes/${encodeURIComponent(id)}/push`),
    )
  }

  /** 解析开关。关闭后其余节点的权重在各线路内重新归一化。 */
  async function toggleDns(id: string, enabled: boolean): Promise<DnsToggleWire> {
    const r = await withBusy(id, enabled ? '恢复解析中' : '暂停解析中', () =>
      http.post<DnsToggleWire>(`/nodes/${encodeURIComponent(id)}/dns`, { enabled }),
    )
    const n = items.value.find((x) => x.id === id)
    if (n) n.dnsEnabled = r.dns_enabled
    return r
  }

  async function probe(id: string): Promise<ProbeWire> {
    const r = await withBusy(id, '探活中', () =>
      http.post<ProbeWire>(`/nodes/${encodeURIComponent(id)}/probe`),
    )
    probes.value = { ...probes.value, [id]: r }
    return r
  }

  /** 下线三步。必须显式确认 —— 后端会拒绝没带 confirm 的请求。 */
  async function drain(id: string): Promise<DrainWire> {
    const r = await withBusy(id, '下线中', () =>
      http.post<DrainWire>(`/nodes/${encodeURIComponent(id)}/drain`, { confirm: true }),
    )
    await fetchAll().catch(() => {})
    return r
  }

  /** 签发一次性接入 Token。token 只在这一次响应里出现，之后任何接口都不回显。 */
  function issueToken(body: {
    node_id: string
    city: string
    vendor: string
    line: string
    public_ip: string
  }): Promise<NodeTokenWire> {
    return http.post<NodeTokenWire>('/nodes/token', body)
  }

  return {
    items,
    loading,
    error,
    busy,
    logs,
    probes,
    byId,
    online,
    drifted,
    fetchAll,
    fetchLogs,
    pushOne,
    toggleDns,
    probe,
    drain,
    issueToken,
    applyHeartbeat,
    recomputeDrift,
  }
})
