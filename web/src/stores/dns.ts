import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { get, put } from '@/api/http'
import type { Node } from '@/api/types'

export interface Weight {
  domain: string
  node: string
  line: string
  weight: number
}

export interface ScheduleResponse {
  domain: string
  lines: string[]
  nodes: Node[]
  weights: Weight[]
  planned?: unknown[]
  live?: unknown[]
  drifted?: boolean
  drift_summary?: string
  /** 读不到线上解析时的原因。有它就说明「有没有漂移」这件事我们不知道。 */
  live_error?: string
}

export interface Row {
  node: string
  city: string
  ip: string
  status: string
  weight: number
}

type Loader = (domain: string) => Promise<ScheduleResponse>
type Saver = (domain: string, body: unknown) => Promise<{ saved: boolean; applied: boolean }>

export const useDnsStore = defineStore('dns', () => {
  const domain = ref('')
  const lines = ref<string[]>([])
  const nodes = ref<Node[]>([])
  const weights = ref<Weight[]>([])
  const drifted = ref(false)
  const driftSummary = ref('')
  const liveError = ref('')
  const loading = ref(false)
  const loadError = ref('')
  const saving = ref(false)
  const saveError = ref('')
  const lastApplied = ref(false)

  let loader: Loader = (d) => get<ScheduleResponse>(`/dns/schedule/${encodeURIComponent(d)}`)
  let saver: Saver = (d, body) =>
    put<{ saved: boolean; applied: boolean }>(`/dns/schedule/${encodeURIComponent(d)}`, body)

  /** __setIO 只供测试替换网络边界。 */
  function __setIO(l: Loader, s: Saver) {
    loader = l
    saver = s
  }

  async function load(d: string) {
    loading.value = true
    loadError.value = ''
    liveError.value = ''
    try {
      const r = await loader(d)
      domain.value = r.domain
      lines.value = r.lines ?? []
      nodes.value = r.nodes ?? []
      weights.value = r.weights ?? []
      drifted.value = r.drifted === true
      driftSummary.value = r.drift_summary ?? ''
      liveError.value = r.live_error ?? ''
    } catch (e) {
      loadError.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  /**
   * syncState 是三态，不是两态。
   *
   * 「读不到线上」和「已同步」必须分开：报「已同步」会让人以为解析是对的，
   * 而我们根本没看到线上是什么样。
   */
  const syncState = computed<'synced' | 'drifted' | 'unknown'>(() => {
    if (liveError.value) return 'unknown'
    return drifted.value ? 'drifted' : 'synced'
  })

  function nodeOf(id: string): Node | undefined {
    return nodes.value.find((n) => n.id === id)
  }

  /** rowsOf 是某条线路上的全部节点行。 */
  function rowsOf(line: string): Row[] {
    return weights.value
      .filter((w) => w.line === line)
      .map((w) => {
        const n = nodeOf(w.node)
        return {
          node: w.node, city: n?.city ?? '—', ip: n?.ip ?? '',
          status: n?.status ?? 'down', weight: w.weight,
        }
      })
  }

  function setWeight(line: string, node: string, weight: number) {
    const i = weights.value.findIndex((w) => w.line === line && w.node === node)
    const v = Math.max(0, Math.floor(weight || 0))
    if (i >= 0) {
      weights.value[i] = { ...weights.value[i], weight: v }
      return
    }
    weights.value = [...weights.value, { domain: domain.value, node, line, weight: v }]
  }

  /**
   * shareOf 是**实际会收到的流量占比**，不是权重除以权重之和。
   *
   * 离线节点自动退出解析，因此不该计入分母：计入的话，两台各 50 掉一台时
   * 界面显示 50%，而实际是 100%——人会以为流量只走了一半。
   */
  function shareOf(line: string, node: string): number {
    const rows = rowsOf(line).filter((r) => r.weight > 0 && r.status !== 'down')
    const total = rows.reduce((a, r) => a + r.weight, 0)
    if (total === 0) return 0
    const me = rows.find((r) => r.node === node)
    if (!me) return 0
    return Math.round((me.weight * 100) / total)
  }

  async function save(apply: boolean) {
    saving.value = true
    saveError.value = ''
    lastApplied.value = false
    try {
      // 提交的是**相对权重**，不是界面上算出来的百分比：存百分比的话，
      // 加一台节点就得把其余全部改一遍
      const r = await saver(domain.value, { weights: weights.value, apply })
      lastApplied.value = r.applied === true
      await load(domain.value)
    } catch (e) {
      // 下发失败**不能**显示成成功：人以为改好了就走了，而线上一点没变
      saveError.value = (e as Error).message
    } finally {
      saving.value = false
    }
  }

  return {
    domain, lines, nodes, weights, drifted, driftSummary, liveError,
    loading, loadError, saving, saveError, lastApplied, syncState,
    load, rowsOf, setWeight, shareOf, save, __setIO,
  }
})
