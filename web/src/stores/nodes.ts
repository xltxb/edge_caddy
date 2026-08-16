import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { get } from '@/api/http'
import type { Frame, Node, NodesResponse } from '@/api/types'

type Fetcher = () => Promise<NodesResponse>

export const useNodesStore = defineStore('nodes', () => {
  const nodes = ref<Node[]>([])
  const baseline = ref('')
  const loading = ref(false)
  const loadError = ref('')

  let fetcher: Fetcher = () => get<NodesResponse>('/nodes')

  /** __setFetcher 只供测试替换网络边界。 */
  function __setFetcher(f: Fetcher) {
    fetcher = f
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

  return { nodes, baseline, loading, loadError, load, applyFrame, filtered, onlineCount, driftedCount, __setFetcher }
})
