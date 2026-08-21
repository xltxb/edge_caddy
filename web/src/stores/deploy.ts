import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { http } from '@/api/http'
import type {
  DeployCreatedWire,
  DeployDetailWire,
  DeployProgressFrame,
  DeployResultWire,
  PreviewWire,
  ValidationError,
} from '@/api/types'

/** WS 断线时降级为每 2s 轮询 GET /deploys/:id（契约 §2）。 */
const POLL_MS = 2_000

/**
 * 进行中的下发 id 记在 sessionStorage 里。
 *
 * 下发在主控侧照常进行，但前端的 `current` 只由 confirm() 建立 —— 刷新一次
 * 它就没了，人会以为下发消失了。记下 id，刷新后用 GET /deploys/:id 重建：
 * `targets` 铺骨架、`results` 合并进去。用 session 而非 local，是因为这条
 * 状态属于这个标签页的这一次操作，不该跨会话留存。
 */
const RUNNING_KEY = 'ec.deploy.running'

function rememberRunning(id: number | null): void {
  try {
    if (id === null) sessionStorage.removeItem(RUNNING_KEY)
    else sessionStorage.setItem(RUNNING_KEY, String(id))
  } catch {
    // 隐私模式下写不了。代价只是刷新后恢复不了，不影响下发本身。
  }
}

function recallRunning(): number | null {
  try {
    const raw = sessionStorage.getItem(RUNNING_KEY)
    if (!raw) return null
    const n = Number(raw)
    return Number.isInteger(n) ? n : null
  } catch {
    return null
  }
}

export type Phase = 'idle' | 'previewing' | 'confirm' | 'running' | 'done'

export interface RunningDeploy {
  id: number
  cfgVersion: string
  resKeys: string[]
  rows: DeployResultWire[]
  phase: 'running' | 'done'
}

export const useDeployStore = defineStore('deploy', () => {
  const phase = ref<Phase>('idle')
  const preview = ref<PreviewWire | null>(null)
  const previewError = ref<string | null>(null)
  const current = ref<RunningDeploy | null>(null)
  /** 后端回来的校验错误，按 res_key → field → reason 索引，供表单落红框。 */
  const fieldErrors = ref<Record<string, Record<string, string>>>({})

  const canDeploy = computed(() => preview.value?.validation.ok === true)

  const doneCount = computed(
    () => current.value?.rows.filter((r) => r.state === 'ok' || r.state === 'fail').length ?? 0,
  )
  const okCount = computed(() => current.value?.rows.filter((r) => r.state === 'ok').length ?? 0)
  const failCount = computed(
    () => current.value?.rows.filter((r) => r.state === 'fail').length ?? 0,
  )
  /** 还会再动的失败行 —— ADR-0005 的 retrying。 */
  const retryingCount = computed(
    () => current.value?.rows.filter((r) => r.state === 'fail' && r.retrying).length ?? 0,
  )

  /** `whitelist[0]` / `spec.ips[2]` → `whitelist` / `spec.ips`。 */
  function basePath(field: string): string {
    return field.replace(/\[\d+\]/g, '')
  }

  /**
   * 把校验错误按 res_key → 字段路径索引，供表单落红框。
   *
   * 后端对数组元素报的是带下标的路径（`whitelist[0]`），而字段表里的路径是
   * `whitelist` —— 只按原样索引的话，红框永远落不到那个输入框上，人只能在
   * 弹层里看到一条报错却不知道去哪儿改。所以两个键都登记：带下标的留着，
   * 基路径也指向同一条原因。
   */
  function indexErrors(errors: ValidationError[]): void {
    const m: Record<string, Record<string, string>> = {}
    for (const e of errors) {
      m[e.res_key] ??= {}
      m[e.res_key]![e.field] = e.reason
      const base = basePath(e.field)
      if (base !== e.field && m[e.res_key]![base] === undefined) {
        m[e.res_key]![base] = e.reason
      }
    }
    fieldErrors.value = m
  }

  /**
   * 取权威渲染并预校验。
   *
   * 校验没过时后端返回的仍是 `code: 0`（契约 §7.1）—— 预览成功地告诉了你
   * 「校验没过」，那不是请求失败。所以这里不靠异常判断，看 validation.ok。
   */
  async function runPreview(resKeys: string[]): Promise<void> {
    phase.value = 'previewing'
    previewError.value = null
    preview.value = null
    try {
      const p = await http.post<PreviewWire>('/deploys/preview', { res_keys: resKeys })
      preview.value = p
      indexErrors(p.validation.errors)
      phase.value = 'confirm'
    } catch (e) {
      previewError.value = e instanceof Error ? e.message : '预览失败'
      phase.value = 'idle'
      throw e
    }
  }

  /** 确认下发。进度全部走 WS；断线时由 startPolling 兜底。 */
  async function confirm(resKeys: string[]): Promise<DeployCreatedWire> {
    const created = await http.post<DeployCreatedWire>('/deploys', { res_keys: resKeys })
    current.value = {
      id: created.deploy_id,
      cfgVersion: created.cfg_version,
      resKeys,
      rows: created.targets.map((node) => ({
        node,
        state: 'wait',
        detail: '待下发',
        retrying: false,
      })),
      phase: 'running',
    }
    phase.value = 'running'
    rememberRunning(created.deploy_id)
    return created
  }

  function applyProgress(frame: DeployProgressFrame): void {
    const c = current.value
    if (!c || frame.data.deploy_id !== c.id) return
    const d = frame.data
    const i = c.rows.findIndex((r) => r.node === d.node)
    const row: DeployResultWire = {
      node: d.node,
      state: d.state,
      detail: d.detail,
      retrying: d.retrying,
    }
    if (i >= 0) c.rows[i] = row
    else c.rows.push(row)

    // 全部落定 = 每一行都到了终态，且没有还会再动的重试行
    const settled = c.rows.every((r) => r.state === 'ok' || r.state === 'fail')
    const stillRetrying = c.rows.some((r) => r.state === 'fail' && r.retrying)
    if (settled && !stillRetrying) {
      c.phase = 'done'
      phase.value = 'done'
      rememberRunning(null)
    }
  }

  /**
   * targets 铺骨架，results 按 node id 合并进去。
   *
   * 未回报的节点保留为「待下发」—— 那一行的存在本身就是信息：还有谁没回来。
   */
  function mergeRows(targets: string[], results: DeployResultWire[]): DeployResultWire[] {
    const byNode = new Map(results.map((r) => [r.node, r]))
    return targets.map(
      (node) => byNode.get(node) ?? { node, state: 'wait', detail: '待下发', retrying: false },
    )
  }

  /**
   * 刷新后恢复进行中的下发。
   *
   * 没有这一步，`targets` 落库对前端就是白做的：正常路径下行是从
   * POST /deploys 的响应来的，本来就完整；`targets` 唯一的用武之地
   * 恰恰是「那次响应已经没了」的场景。
   */
  async function resume(): Promise<boolean> {
    if (current.value) return true
    const id = recallRunning()
    if (id === null) return false
    try {
      const d = await http.get<DeployDetailWire>(`/deploys/${id}`)
      if (d.phase === 'done') {
        rememberRunning(null)
        return false
      }
      current.value = {
        id: d.id,
        cfgVersion: d.cfg_version,
        resKeys: d.res_keys,
        rows: mergeRows(d.targets, d.results),
        phase: d.phase,
      }
      phase.value = 'running'
      return true
    } catch {
      rememberRunning(null)
      return false
    }
  }

  /* ── 轮询降级 ── */

  let timer: ReturnType<typeof setInterval> | null = null

  function startPolling(): void {
    if (timer || !current.value) return
    timer = setInterval(() => void pollOnce(), POLL_MS)
  }

  function stopPolling(): void {
    if (timer) clearInterval(timer)
    timer = null
  }

  async function pollOnce(): Promise<void> {
    const c = current.value
    if (!c) return stopPolling()
    try {
      // 详情的 results[] 与 WS 帧一一对应，所以进度组件两条数据源共用一套渲染。
      // 但进行中时它是**部分**结果（后端逐条落库），整体替换会把还没回报的
      // 节点整行抹掉 —— 那正好是降级时最需要看见的「还有谁没回来」。
      const d = await http.get<DeployDetailWire>(`/deploys/${c.id}`)
      c.rows = mergeRows(d.targets, d.results)
      c.phase = d.phase
      if (d.phase === 'done') {
        phase.value = 'done'
        rememberRunning(null)
        stopPolling()
      }
    } catch {
      // 轮询失败就等下一轮，不打断界面
    }
  }

  function reset(): void {
    stopPolling()
    rememberRunning(null)
    phase.value = 'idle'
    preview.value = null
    previewError.value = null
    current.value = null
    fieldErrors.value = {}
  }

  return {
    phase,
    preview,
    previewError,
    current,
    fieldErrors,
    canDeploy,
    doneCount,
    okCount,
    failCount,
    retryingCount,
    runPreview,
    confirm,
    applyProgress,
    resume,
    mergeRows,
    startPolling,
    stopPolling,
    /** 导出是为了能被确定性地测到 —— 降级路径在真实环境里很难稳定复现。 */
    pollOnce,
    reset,
  }
})
