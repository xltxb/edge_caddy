import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { http, errorText } from '@/api/http'
import type {
  DnsSyncWire,
  DnsToggleWire,
  DrainWire,
  HeartbeatFrame,
  LogLevel,
  NodeDeleteWire,
  NodeTokenWire,
  NodeUpdateWire,
  NodesPageWire,
  Paged,
  ProbeWire,
  RejoinWire,
} from '@/api/types'
import type { NodeUpdateBody } from '@/api/requests'
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
  /** 取日志失败的原因，按节点。**与「取到了但是空的」是两回事。** */
  const logsError = ref<Record<string, string>>({})
  /** 最近一次探活结果。Caddy Admin 与隧道分开报 —— 两种故障处置不同。 */
  const probes = ref<Record<string, ProbeWire>>({})
  /**
   * 服务商那边有没有反映我们的解析安排。null = 还没取到。
   *
   * 节点上的 `dns_enabled` 是**本地标志位**，它决定归一化里谁参与；解析记录真的
   * 变没变是另一件事。没同步时，一个「已退出解析」的徽标就是在撒谎 —— 那台机器
   * 照旧在解析里。
   *
   * 这份来自 `GET /nodes` 顶层，与列表同一个响应，所以不用额外发请求；早先我是
   * 从 `/dns/weights` 的 `capabilities.kind` 推「压根没配」，那推不出「上次同步
   * 失败了」。
   */
  const dnsSync = ref<DnsSyncWire | null>(null)

  const byId = computed(() => new Map(items.value.map((n) => [n.id, n])))
  /**
   * 在线 = status 为 ok。
   *
   * 把 warn 也算进来的话，「N/M 在线」与脚注「异常 X 个 · 离线 Y 个」对不上账 ——
   * 异常节点会同时被算成在线、又被点名为异常。一个算不平的账比少一个数字更糟。
   */
  const online = computed(() => items.value.filter((n) => n.status === 'ok'))
  const drifted = computed(() => items.value.filter((n) => n.drift))

  async function fetchAll(): Promise<void> {
    loading.value = true
    error.value = null
    try {
      const page = await http.get<NodesPageWire>('/nodes')
      items.value = page.items.map((w) => fromNodeWire(w))
      dnsSync.value = page.dns_sync ?? null
    } catch (e) {
      error.value = errorText(e, '加载节点失败')
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
    // **drift 在这里就算出来**：它不在心跳帧里，但算它要的两样东西此刻都在手上
    // ——刚报上来的版本号，和这个 store 记着的基线。
    //
    // 这一行原先是一句注释：「由 overview store 的基线变化触发重算」。
    // 那个触发器不存在（issue #50），于是 drift 停在登录那一刻的值，
    // 两个方向都会错：刚下发成功的节点继续挂着「未收到最近下发」，
    // 真漂移的节点一片干净。
    // **基线没到位时不动它。** `driftOf` 那时返回 false，而用它覆写等于把
    // 服务端 REST 报的那份真话换成一个算不出来的值——节点列表一片干净、
    // 漂移 KPI 归零，而真相是「还没拿到基线」。
    // 「『还不知道』被显示成『没问题』」是这个仓库反复记的那一族。
    if (baseline.value) n.drift = driftOf(n.cfgVersion)

    const next = [...n.cpuSeries, Math.round(d.cpu)]
    n.cpuSeries = next.slice(-SERIES_LEN)
  }

  /**
   * baseline 是**这个 store 自己记着的基线**。
   *
   * 漂移 = 上报版本号 ≠ 基线（ADR-0002），两个操作数缺一不可。基线原先只活在
   * overview store 里，nodes 这边每次要算 drift 都得有人把它端过来——而那个
   * 「有人」只在登录时出现过一次。记在这里之后，两个操作数任何一个变了都算得动。
   */
  const baseline = ref<string>('')

  function driftOf(cfgVersion: string): boolean {
    // 基线还没到位时不谈漂移：那时「不一致」说的是「还不知道」。
    if (!baseline.value) return false
    return cfgVersion !== baseline.value
  }

  /** 基线变了要重算全体 drift —— 版本号比对是漂移的**唯一**依据（ADR-0002）。 */
  function setBaseline(v: string): void {
    baseline.value = v
    for (const n of items.value) n.drift = driftOf(n.cfgVersion)
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

  /**
   * 取一台节点的 Agent 日志。
   *
   * **取失败要记下来，不能让它长得像「这台机器没有日志」。** 调用方原先写的是
   * `.catch(() => {})`，于是端点 404 的时候面板显示「暂无日志。」—— 而那四个字
   * 说的是「这台机器安静得很」，真相是「这个功能没接上」。两句话会把人引向
   * 完全不同的地方：前者让人放心，后者让人去查。
   *
   * （`GET /nodes/:id/logs` 目前在主控上确实不存在 —— 契约里格式完整、从来没
   * 注册过，是后端扫「写了但没人读」时抓出来的。这里不假装它存在，也不假装
   * 它返回了空。）
   */
  async function fetchLogs(id: string): Promise<void> {
    try {
      const page = await http.get<Paged<{ at: string; level: LogLevel; msg: string }>>(
        `/nodes/${encodeURIComponent(id)}/logs`,
      )
      logs.value = { ...logs.value, [id]: page.items }
      const e = { ...logsError.value }
      delete e[id]
      logsError.value = e
    } catch (e) {
      logsError.value = { ...logsError.value, [id]: errorText(e, '取日志失败') }
    }
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

  /**
   * 重新上线：撤销下线标记。
   *
   * **解析不会跟着打开**（契约 §4 的 `POST /nodes/:id/rejoin`，响应里的 detail
   * 就是这么说的）——能接入不等于该马上分流量：它刚回来，
   * 配置可能还是旧的。所以这里也不顺手替人打开，返回的 detail 会说明这一点。
   */
  async function rejoin(id: string): Promise<RejoinWire> {
    const r = await withBusy(id, '重新上线中', () =>
      http.post<RejoinWire>(`/nodes/${encodeURIComponent(id)}/rejoin`),
    )
    await fetchAll().catch(() => {})
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

  /**
   * 改元数据。**四项都必填，而且发不出 node_id** —— 类型挡着（`NodeUpdateBody`）。
   *
   * 就地更新而不是重拉全表：响应里这四项都回了，而重拉会把整页的心跳年龄、
   * CPU 序列一起换掉 —— 人刚改完一个城市名，满屏数字跳一下，看起来像发生了
   * 别的事。
   *
   * **返回值要交给调用方**：`dns_synced` 为 false 有两种完全不同的意思
   * （「这次改动跟解析无关」和「解析该变而没变成」），store 判不了，
   * 那取决于人到底改没改 IP。
   */
  async function updateNode(id: string, body: NodeUpdateBody): Promise<NodeUpdateWire> {
    const r = await withBusy(id, '保存中', () =>
      http.put<NodeUpdateWire>(`/nodes/${encodeURIComponent(id)}`, body),
    )
    const n = items.value.find((x) => x.id === id)
    if (n) {
      n.city = r.city
      n.vendor = r.vendor
      n.line = r.line
      n.ip = r.public_ip
    }
    return r
  }

  /**
   * 删记录。**必须先下线**，否则后端拒绝。
   *
   * 那不是一道礼节性的确认：一台还连着的机器手里有隧道证书，删掉记录之后它会
   * 重连、被按证书认出来、然后在一张不存在的行上写心跳 —— `TouchHeartbeat` 是
   * UPDATE，影响 0 行，**不报错**。结果是一台连着、在服务、而控制台上看不见的
   * 机器（契约 §4）。
   *
   * 成功后就地移除，不重拉：那一行已经不存在了，让它当场消失比等一轮请求诚实。
   */
  async function removeNode(id: string): Promise<NodeDeleteWire> {
    const r = await withBusy(id, '删除中', () =>
      http.del<NodeDeleteWire>(`/nodes/${encodeURIComponent(id)}`),
    )
    items.value = items.value.filter((x) => x.id !== id)
    return r
  }

  return {
    items,
    loading,
    error,
    busy,
    logs,
    logsError,
    probes,
    dnsSync,
    byId,
    online,
    drifted,
    fetchAll,
    fetchLogs,
    pushOne,
    toggleDns,
    probe,
    drain,
    rejoin,
    issueToken,
    updateNode,
    removeNode,
    applyHeartbeat,
    baseline,
    setBaseline,
  }
})
