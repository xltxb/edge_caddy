import { defineStore } from 'pinia'
import { ref } from 'vue'
import { get, post } from '@/api/http'

export interface AggregatedCert {
  domain: string
  not_after: string
  days_left: number
  /** crit / warn / ok。**由后端给**，前端不自己算。 */
  severity: string
  issuer: string
  key_type: string
  node_count: number
  has_stale: boolean
  stale_nodes: string[]
  oldest_age_sec: number
}

export interface CertsResponse {
  certs: AggregatedCert[]
  bands: { crit_below_days: number; warn_below_days: number }
  stale_after_sec: number
}

export interface RenewResult {
  domain: string
  ok: boolean
  detail: string
}

type Loader = () => Promise<CertsResponse>
type Renewer = (domain: string) => Promise<unknown>
type RenewAll = () => Promise<{ results: RenewResult[] }>

/**
 * 三档的文字标签。
 *
 * 「色 + 文字」双编码：只有颜色的话，色觉障碍的人看到的是三块一样的灰，
 * 而这三块的含义是「今天就得处理」「这周处理」「不用管」。
 */
const LABELS: Record<string, string> = {
  crit: '急需处理',
  warn: '即将到期',
  ok: '正常',
}

export const useCertsStore = defineStore('certs', () => {
  const certs = ref<AggregatedCert[]>([])
  const bands = ref({ crit_below_days: 7, warn_below_days: 30 })
  const staleAfterSec = ref(600)
  const loading = ref(false)
  const error = ref('')
  /** busy 是正在续期的域名；'*' 表示全部续期检查进行中。 */
  const busy = ref('')
  const results = ref<RenewResult[]>([])

  let loader: Loader = () => get<CertsResponse>('/certs')
  let renewer: Renewer = (d) => post<unknown>(`/certs/${encodeURIComponent(d)}/renew`)
  let renewAllFn: RenewAll = () => post<{ results: RenewResult[] }>('/certs/renew-all')

  /** __setIO 只供测试替换网络边界。 */
  function __setIO(l: Loader, r: Renewer, a: RenewAll) {
    loader = l
    renewer = r
    renewAllFn = a
  }

  async function load() {
    loading.value = true
    error.value = ''
    try {
      const r = await loader()
      certs.value = r.certs ?? []
      if (r.bands) bands.value = r.bands
      if (r.stale_after_sec) staleAfterSec.value = r.stale_after_sec
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  function label(severity: string): string {
    return LABELS[severity] ?? '未知'
  }

  /**
   * staleHint 说明数据有多旧、来自哪几台。
   *
   * 不假装是最新的：一台掉了三天的机器上的证书状态就是三天前的，
   * 把它和刚上报的混在一起显示，等于用过时数据做判断。
   */
  function staleHint(c: AggregatedCert): string {
    if (!c.has_stale) return ''
    const nodes = (c.stale_nodes ?? []).join('、')
    return `${nodes} 的数据已是 ${humanAge(c.oldest_age_sec)}之前的，未必反映现状`
  }

  function humanAge(sec: number): string {
    if (sec >= 86400) return `${Math.floor(sec / 86400)} 天`
    if (sec >= 3600) return `${Math.floor(sec / 3600)} 小时`
    return `${Math.max(1, Math.floor(sec / 60))} 分钟`
  }

  async function renew(domain: string) {
    busy.value = domain
    try {
      await renewer(domain)
      results.value = [{ domain, ok: true, detail: '已续期' }, ...results.value]
      await load()
    } catch (e) {
      // 失败原因原样留着：「DNS 凭据无效」和「域名不在这个账号下」
      // 是两件事，处理方式完全不同
      results.value = [{ domain, ok: false, detail: (e as Error).message }, ...results.value]
    } finally {
      busy.value = ''
    }
  }

  async function renewAll() {
    busy.value = '*'
    try {
      const r = await renewAllFn()
      // 逐项反馈，不是一句「全部成功」：运维需要知道哪几个好了、哪几个没好
      results.value = r.results ?? []
      await load()
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      busy.value = ''
    }
  }

  return {
    certs, bands, staleAfterSec, loading, error, busy, results,
    load, renew, renewAll, label, staleHint, humanAge, __setIO,
  }
})
