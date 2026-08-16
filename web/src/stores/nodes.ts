import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { get, post } from '@/api/http'
import type { Frame, Node, NodesResponse } from '@/api/types'

type Fetcher = () => Promise<NodesResponse>
type Poster = (path: string, body?: unknown) => Promise<unknown>

/** 节点操作动词。与后端 /nodes/:id/{probe,push,drain} 一一对应。 */
export type NodeVerb = 'probe' | 'push' | 'drain' | 'logs'

/** NodeDetail 是节点行展开显示的内容，全部来自**同一次**探活回报。 */
export interface NodeDetail {
  rttMs?: number
  cfgVersion?: string
  caddyOk?: boolean
  caddyDetail?: string
  logs: string[]
  /** 探活本身没成功时的原因。有它就说明下面几项没有意义。 */
  error?: string
}

export interface OpRecord {
  verb: NodeVerb
  node: string
  ok: boolean
  detail: string
}

export const useNodesStore = defineStore('nodes', () => {
  const nodes = ref<Node[]>([])
  const baseline = ref('')
  const loading = ref(false)
  const loadError = ref('')

  /** busyOp 形如 `probe:node-hk-01`。精确到「哪个节点的哪个操作」——
   *  只有一个全局 boolean 的话，点了香港那台，整张表的按钮都会转圈。 */
  const busyOp = ref('')
  const opLog = ref<OpRecord[]>([])
  const detail = ref<Record<string, NodeDetail>>({})
  const expanded = ref('')
  /** pendingDrain 是等待确认的下线目标。空串表示没有待确认的。 */
  const pendingDrain = ref('')

  let fetcher: Fetcher = () => get<NodesResponse>('/nodes')
  let poster: Poster = (path, body) => post<unknown>(path, body)

  /** __setFetcher 只供测试替换网络边界。 */
  function __setFetcher(f: Fetcher) {
    fetcher = f
  }

  /** __setPoster 只供测试替换网络边界。 */
  function __setPoster(p: Poster) {
    poster = p
  }

  /**
   * runOp 是节点操作的**唯一**入口：行内按钮与命令面板都走这里。
   *
   * 两条路径各自发请求的话，忙碌态、错误提示、审计上下文就会分叉——
   * 面板执行完按钮还亮着，是这类界面最典型的 bug。
   */
  async function runOp(verb: NodeVerb, node: string, body?: unknown): Promise<OpRecord> {
    const key = `${verb}:${node}`
    // 连点两下「重推」不该真推两次
    if (busyOp.value === key) return opLog.value[0] ?? { verb, node, ok: false, detail: '进行中' }
    busyOp.value = key
    let rec: OpRecord
    try {
      const r = (await poster(`/nodes/${encodeURIComponent(node)}/${verb}`, body)) as { detail?: string } | null
      rec = { verb, node, ok: true, detail: r?.detail ?? '已完成' }
    } catch (e) {
      // 失败要留痕，不能静默吞掉：节点未连接返回 404，
      // 那正是运维最需要看到的一条。
      rec = { verb, node, ok: false, detail: (e as Error).message }
    } finally {
      busyOp.value = ''
    }
    opLog.value.unshift(rec)
    return rec
  }

  /**
   * expand 展开一行：探活一次，把生效配置、Caddy 可达性、最近日志一起取回。
   *
   * 三样信息来自同一次回报——分三次取会各自看到不同的瞬间，
   * 拼出来的画面从未真实存在过。
   */
  async function expand(node: string) {
    expanded.value = expanded.value === node ? '' : node
    if (expanded.value !== node) return
    busyOp.value = `expand:${node}`
    try {
      const r = (await poster(`/nodes/${encodeURIComponent(node)}/probe`)) as {
        rtt_ms?: number; cfg_version?: string; caddy_ok?: boolean
        caddy_detail?: string; logs?: string[]
      }
      detail.value[node] = {
        rttMs: r.rtt_ms, cfgVersion: r.cfg_version, caddyOk: r.caddy_ok,
        caddyDetail: r.caddy_detail, logs: r.logs ?? [],
      }
    } catch (e) {
      // 只留错误，不伪造一份空状态：留空壳会让界面显示「Caddy 不可达」，
      // 把「没问上」说成「问到了，答案是坏的」。
      detail.value[node] = { logs: [], error: (e as Error).message }
    } finally {
      busyOp.value = ''
    }
  }

  /**
   * askDrain 只登记意图，不执行。
   *
   * 下线会把节点摘出去，而它此刻正在承接流量。行内按钮和命令面板都经过这里——
   * 「敲命令的人知道自己在做什么」不成立：面板正是最容易手滑的入口。
   */
  function askDrain(node: string) {
    pendingDrain.value = node
  }

  function cancelDrain() {
    pendingDrain.value = ''
  }

  async function confirmDrain(reason?: string) {
    const node = pendingDrain.value
    if (!node) return
    pendingDrain.value = ''
    await runOp('drain', node, reason ? { reason } : undefined)
  }

  async function load() {
    loading.value = true
    loadError.value = ''
    try {
      const r = await fetcher()
      // 负载三项来自心跳，列表接口不给。补 0 而不是留 undefined：
      // 界面要显示 "CPU 0.0%" 而不是 "CPU NaN%"。
      nodes.value = (r.nodes ?? []).map((n) => ({ ...n, cpu: n.cpu ?? 0, mem: n.mem ?? 0, conns: n.conns ?? 0 }))
      baseline.value = r.baseline ?? ''
    } catch (e) {
      loadError.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  /**
   * applyFrame 处理一条 WS 帧。
   *
   * 心跳**就地更新**已有节点，不追加：追加的话节点每 3 秒长出一行，
   * 一分钟后列表里全是同一个节点，而刚打开时完全正常。
   *
   * 未知节点的心跳直接忽略——凭一条心跳造出的行没有城市、厂商、IP，
   * 是个幽灵。节点的完整信息只来自 /nodes。
   */
  function applyFrame(frame: Frame) {
    if (frame.type !== 'heartbeat') return
    const d = frame.data as { id?: string; cpu?: number; mem?: number; conns?: number; hb_ms?: number }
    if (!d.id) return
    const i = nodes.value.findIndex((n) => n.id === d.id)
    if (i < 0) return
    const cur = nodes.value[i]
    nodes.value[i] = {
      ...cur,
      cpu: d.cpu ?? cur.cpu,
      mem: d.mem ?? cur.mem,
      conns: d.conns ?? cur.conns,
      status: 'ok', // 能发心跳就是活的
    }
  }

  /** filtered 按 KPI 卡片筛选节点（前端文档 §3：query 同步 ?filter=）。 */
  function filtered(kind: string) {
    switch (kind) {
      case 'online':
        return nodes.value.filter((n) => n.status !== 'down')
      case 'down':
        return nodes.value.filter((n) => n.status === 'down')
      case 'drifted':
        return nodes.value.filter((n) => n.drifted)
      default:
        return nodes.value
    }
  }

  const onlineCount = computed(() => nodes.value.filter((n) => n.status !== 'down').length)
  const driftedCount = computed(() => nodes.value.filter((n) => n.drifted).length)

  return {
    nodes, baseline, loading, loadError, load, applyFrame, filtered, onlineCount, driftedCount,
    busyOp, opLog, runOp, detail, expanded, expand,
    pendingDrain, askDrain, cancelDrain, confirmDrain,
    __setFetcher, __setPoster,
  }
})
