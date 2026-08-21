import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { http } from '@/api/http'
import type {
  DraftMeta,
  DraftsWire,
  Paged,
  PolicyWire,
  ResKind,
  RouteWire,
  RuleWire,
} from '@/api/types'
import { applyEdit, changeCount as countPatch, merge, type Patch } from '@/workbench/draft'

/** 草稿写回后端的节流窗口。每敲一个字符就 PUT 一次太吵，但也不能等到点下发才存。 */
const PERSIST_MS = 400

export interface ResourceItem {
  key: string
  kind: ResKind
  label: string
  /** 分组标题，资源树按它分段。 */
  group: string
  dirty: boolean
  changes: number
  /** version 0 = 尚未下发到任何节点 */
  isNew: boolean
}

export const useConfigStore = defineStore('config', () => {
  const routes = ref<RouteWire[]>([])
  const rules = ref<RuleWire[]>([])
  const policies = ref<PolicyWire[]>([])
  const patches = ref<Record<string, Patch>>({})
  const updated = ref<Record<string, DraftMeta>>({})
  const loading = ref(false)
  const error = ref<string | null>(null)

  /* ── 基线（live）查找 ── */

  const liveByKey = computed<Record<string, Record<string, unknown>>>(() => {
    const m: Record<string, Record<string, unknown>> = {}
    for (const r of routes.value) m[`route:${r.domain}`] = r as unknown as Record<string, unknown>
    for (const r of rules.value) m[`rule:${r.id}`] = r as unknown as Record<string, unknown>
    for (const p of policies.value) m[`global:${p.id}`] = p as unknown as Record<string, unknown>
    return m
  })

  /** 基线值（未叠加草稿）。 */
  function live(key: string): Record<string, unknown> | undefined {
    return liveByKey.value[key]
  }

  /** 有效值 = 基线 + 草稿。界面上看到的、渲染可读表示用的都是它。 */
  function effective(key: string): Record<string, unknown> | undefined {
    const base = live(key)
    if (!base) return undefined
    return merge(base, patches.value[key])
  }

  const totalChanges = computed(() =>
    Object.values(patches.value).reduce((n, p) => n + countPatch(p), 0),
  )

  const dirtyKeys = computed(() =>
    Object.keys(patches.value).filter((k) => countPatch(patches.value[k]) > 0),
  )

  function changesOf(key: string): number {
    return countPatch(patches.value[key])
  }

  /* ── 资源树 ── */

  const tree = computed<ResourceItem[]>(() => {
    const items: ResourceItem[] = []
    for (const r of routes.value) {
      const key = `route:${r.domain}`
      items.push({
        key,
        kind: 'route',
        label: r.domain,
        group: '反代路由',
        dirty: changesOf(key) > 0,
        changes: changesOf(key),
        isNew: r.version === 0,
      })
    }
    for (const r of rules.value) {
      const key = `rule:${r.id}`
      items.push({
        key,
        kind: 'rule',
        label: r.name,
        group: '访问规则',
        dirty: changesOf(key) > 0,
        changes: changesOf(key),
        isNew: r.version === 0,
      })
    }
    for (const p of policies.value) {
      const key = `global:${p.id}`
      items.push({
        key,
        kind: 'global',
        label: p.name,
        group: '全局策略',
        dirty: changesOf(key) > 0,
        changes: changesOf(key),
        isNew: false,
      })
    }
    return items
  })

  /* ── 读 ── */

  /** 哪几类资源这次没取到。空数组 = 全都拿到了。 */
  const failedParts = ref<string[]>([])

  /**
   * 拉取全部配置资源。
   *
   * 用 `allSettled` 而不是 `all`：这几类资源**互相独立**，一个失败不该把
   * 已经成功的那几个也丢掉。用 `all` 时，`/policies/tls` 偶发 500 会让工作台
   * 整个空掉 —— 连好端端返回了 200 的路由和草稿一起没了，而界面只会说
   * 「加载配置失败」，看不出是哪一块出的问题。
   */
  async function fetchAll(): Promise<void> {
    loading.value = true
    error.value = null
    const failed: string[] = []

    const [rt, rl, tls, log, dr] = await Promise.allSettled([
      http.get<Paged<RouteWire>>('/routes'),
      http.get<Paged<RuleWire>>('/rules'),
      http.get<PolicyWire>('/policies/tls'),
      http.get<PolicyWire>('/policies/log'),
      http.get<DraftsWire>('/drafts'),
    ])

    if (rt.status === 'fulfilled') routes.value = rt.value.items
    else failed.push('反代路由')

    if (rl.status === 'fulfilled') rules.value = rl.value.items
    else failed.push('访问规则')

    const pols: PolicyWire[] = []
    if (tls.status === 'fulfilled') pols.push(tls.value)
    else failed.push('TLS 策略')
    if (log.status === 'fulfilled') pols.push(log.value)
    else failed.push('日志策略')
    policies.value = pols

    if (dr.status === 'fulfilled') {
      patches.value = dr.value.items
      updated.value = dr.value.updated
    } else {
      failed.push('草稿')
    }

    failedParts.value = failed
    // 全军覆没才算「加载失败」；部分失败让能用的先用起来，并说清缺了什么
    if (failed.length === 5) {
      error.value = '加载配置失败'
    }
    loading.value = false
  }

  /* ── 写 ── */

  const timers = new Map<string, ReturnType<typeof setTimeout>>()

  function schedulePersist(key: string): void {
    const t = timers.get(key)
    if (t) clearTimeout(t)
    timers.set(
      key,
      setTimeout(() => {
        timers.delete(key)
        void persist(key)
      }, PERSIST_MS),
    )
  }

  async function persist(key: string): Promise<void> {
    // Partial 为空对象时后端会删掉该草稿行，等价于「这个资源没有未下发改动」
    await http.put(`/drafts/${encodeURIComponent(key)}`, patches.value[key] ?? {}).catch(() => {
      // 草稿写回失败不该打断输入。下一次输入会重试，点下发前还会整体重取一次。
    })
  }

  /**
   * 把所有还没落地的草稿立刻写回。
   *
   * 节流窗口是 400ms，够短到看不出来、也够长到能丢东西：改一个字段然后立刻
   * 切页或刷新，那次写就没发出去过 —— 界面上改动还在（内存里），回来之后
   * 却不见了。**静默丢失用户刚敲的东西**是这里最不能接受的失败方式。
   *
   * `keepalive` 让请求能在页面卸载过程中继续发完（beforeunload 用）。
   */
  function flush(opts: { keepalive?: boolean } = {}): void {
    const keys = [...timers.keys()]
    for (const k of keys) {
      const t = timers.get(k)
      if (t) clearTimeout(t)
      timers.delete(k)
      if (opts.keepalive) {
        // 卸载途中不能 await，只能靠 keepalive 把它送出去
        void fetch(`/api/v1/drafts/${encodeURIComponent(k)}`, {
          method: 'PUT',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(patches.value[k] ?? {}),
          keepalive: true,
        }).catch(() => {})
      } else {
        void persist(k)
      }
    }
  }

  /**
   * 改一个字段。
   *
   * 值改回与线上一致时 `applyEdit` 会把该键剪掉，剪空了这里再删掉整条草稿——
   * 留一个等值的键会让蓝点和「待下发」虚报（契约 §6.4）。
   */
  function setField(key: string, path: string, value: unknown): void {
    const base = live(key)
    if (!base) return
    const next = applyEdit(base, patches.value[key] ?? {}, path, value)
    const copy = { ...patches.value }
    if (Object.keys(next).length === 0) delete copy[key]
    else copy[key] = next
    patches.value = copy
    schedulePersist(key)
  }

  /** 放弃某个资源的草稿。 */
  function revert(key: string): void {
    const copy = { ...patches.value }
    delete copy[key]
    patches.value = copy
    schedulePersist(key)
  }

  /** 放弃全部草稿。 */
  async function discardAll(): Promise<void> {
    await http.del('/drafts')
    patches.value = {}
    updated.value = {}
  }

  /**
   * 设置一条 service_secret 规则的共享密钥。
   *
   * **这条不走草稿。** 草稿是 `PUT /drafts/:key` 存在主控上、由 `GET /drafts`
   * 全局回显的（契约 §6.4）—— 密钥进草稿就等于被回显，那正是后端把它挪出
   * `spec` 要躲的那件事。它走 `PUT /rules/:id` 的顶层 `secret`：直写、加封存库、
   * 任何读接口都不回显，只回 `spec.secret_configured` 布尔。
   *
   * 提交的规则体取 **live 而不是 effective**：这条规则上可能正压着未下发的草稿，
   * 把 effective 发出去，等于「保存一个密钥」顺手让半截改动绕过下发流水线生效了。
   */
  async function setRuleSecret(id: string, secret: string): Promise<void> {
    const cur = rules.value.find((r) => r.id === id)
    if (!cur) throw new Error(`没有这条规则：${id}`)
    await http.put(`/rules/${id}`, { ...cur, secret })
    await fetchAll().catch(() => {})
  }

  /**
   * 删除一条访问规则。
   *
   * 后端会连同这条规则的草稿一起清掉 —— 留一份指向已删资源的草稿，会让顶栏的
   * 「有几处未下发改动」算上一个再也下发不出去的东西。所以本地也要跟着清，
   * 否则那个数字要等到下次 fetchAll 才对得上。
   */
  async function deleteRule(id: string): Promise<void> {
    await http.del(`/rules/${id}`)
    const copy = { ...patches.value }
    delete copy[`rule:${id}`]
    patches.value = copy
    await fetchAll().catch(() => {})
  }

  /** 下发成功后清掉已下发的那几条，并把版本推进。 */
  function commit(keys: string[]): void {
    const copy = { ...patches.value }
    for (const k of keys) delete copy[k]
    patches.value = copy
  }

  return {
    setRuleSecret,
    deleteRule,
    routes,
    rules,
    policies,
    patches,
    updated,
    loading,
    error,
    failedParts,
    tree,
    totalChanges,
    dirtyKeys,
    live,
    effective,
    changesOf,
    fetchAll,
    setField,
    flush,
    revert,
    discardAll,
    commit,
  }
})
