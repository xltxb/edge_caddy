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

  async function fetchAll(): Promise<void> {
    loading.value = true
    error.value = null
    try {
      const [rt, rl, tls, log, dr] = await Promise.all([
        http.get<Paged<RouteWire>>('/routes'),
        http.get<Paged<RuleWire>>('/rules'),
        http.get<PolicyWire>('/policies/tls'),
        http.get<PolicyWire>('/policies/log'),
        http.get<DraftsWire>('/drafts'),
      ])
      routes.value = rt.items
      rules.value = rl.items
      policies.value = [tls, log]
      patches.value = dr.items
      updated.value = dr.updated
    } catch (e) {
      error.value = e instanceof Error ? e.message : '加载配置失败'
      throw e
    } finally {
      loading.value = false
    }
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

  /** 下发成功后清掉已下发的那几条，并把版本推进。 */
  function commit(keys: string[]): void {
    const copy = { ...patches.value }
    for (const k of keys) delete copy[k]
    patches.value = copy
  }

  return {
    routes,
    rules,
    policies,
    patches,
    updated,
    loading,
    error,
    tree,
    totalChanges,
    dirtyKeys,
    live,
    effective,
    changesOf,
    fetchAll,
    setField,
    revert,
    discardAll,
    commit,
  }
})
