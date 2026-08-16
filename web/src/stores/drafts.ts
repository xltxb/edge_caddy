import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { get, put } from '@/api/http'
import type { Route } from '@/api/types'

/** 草稿是叠加在线上值之上的 Partial。 */
export type RoutePatch = Partial<Route>

export interface DraftRow {
  res_key: string
  patch: RoutePatch
  updated_by: string
}

/** 资源键：route:api.example.com */
export const routeKey = (domain: string) => `route:${domain}`

interface Fetchers {
  listRoutes: () => Promise<Route[]>
  listDrafts: () => Promise<DraftRow[]>
  putDraft: (key: string, patch: RoutePatch) => Promise<void>
}

export const useDraftsStore = defineStore('drafts', () => {
  const live = ref<Route[]>([])
  const drafts = ref<Record<string, RoutePatch>>({})
  const authors = ref<Record<string, string>>({})
  const loading = ref(false)
  const loadError = ref('')

  let fetchers: Fetchers = {
    listRoutes: async () => (await get<{ routes: Route[] }>('/routes')).routes ?? [],
    listDrafts: async () => (await get<{ drafts: DraftRow[] }>('/drafts')).drafts ?? [],
    putDraft: async (key, patch) => {
      await put(`/drafts/${encodeURIComponent(key)}`, { patch })
    },
  }

  /** __setFetchers 只供测试替换网络边界。 */
  function __setFetchers(f: Fetchers) {
    fetchers = f
  }

  async function load() {
    loading.value = true
    loadError.value = ''
    try {
      live.value = await fetchers.listRoutes()
      const ds = await fetchers.listDrafts()
      const next: Record<string, RoutePatch> = {}
      const by: Record<string, string> = {}
      for (const d of ds) {
        next[d.res_key] = d.patch
        by[d.res_key] = d.updated_by
      }
      drafts.value = next
      authors.value = by
    } catch (e) {
      loadError.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  function liveOf(key: string): Route | undefined {
    return live.value.find((r) => routeKey(r.domain) === key)
  }

  /** effective 是线上值叠加草稿后的当前值。 */
  function effective(key: string): Route | undefined {
    const base = liveOf(key)
    if (!base) return undefined
    return { ...base, ...(drafts.value[key] ?? {}) }
  }

  /**
   * setField 记录一处改动。
   *
   * 值改回与线上一致时**删掉该键**。不删的话会留下一处推不掉的幽灵改动：
   * 资源树的标记不消失、diff 是空的、反复点推送也去不掉——因为内容确实没变。
   */
  function setField<K extends keyof Route>(key: string, field: K, value: Route[K]) {
    const base = liveOf(key)
    if (!base) return
    const patch = { ...(drafts.value[key] ?? {}) }

    if (sameValue(field, value, base[field])) delete patch[field]
    else patch[field] = value

    const next = { ...drafts.value }
    if (Object.keys(patch).length === 0) delete next[key]
    else next[key] = patch
    drafts.value = next

    void fetchers.putDraft(key, patch)
  }

  function revert(key: string) {
    const next = { ...drafts.value }
    delete next[key]
    drafts.value = next
    void fetchers.putDraft(key, {})
  }

  const dirtyKeys = computed(() => Object.keys(drafts.value).sort())
  const totalChanges = computed(() =>
    dirtyKeys.value.reduce((n, k) => n + Object.keys(drafts.value[k]).length, 0),
  )

  function isDirty(key: string) {
    return Object.keys(drafts.value[key] ?? {}).length > 0
  }
  function changeCount(key: string) {
    return Object.keys(drafts.value[key] ?? {}).length
  }
  function authorOf(key: string) {
    return authors.value[key] ?? ''
  }

  return {
    live, drafts, loading, loadError, load, effective, liveOf, setField, revert,
    dirtyKeys, totalChanges, isDirty, changeCount, authorOf, __setFetchers,
  }
})

/**
 * sameValue 判断两个值是否「实质相同」。
 *
 * 白名单先规范化（去首尾空白、丢空行）再比：用户在文本框里敲回车是常态，
 * 把它算成一处待下发的改动会让「有几处改动」这个数字失去意义。
 */
function sameValue<K extends keyof Route>(field: K, a: Route[K], b: Route[K]): boolean {
  if (field === 'wl') {
    return JSON.stringify(normalize(a as string[])) === JSON.stringify(normalize(b as string[]))
  }
  return JSON.stringify(a) === JSON.stringify(b)
}

export function normalize(list: string[] | undefined): string[] {
  return (list ?? []).map((s) => String(s).trim()).filter(Boolean)
}
