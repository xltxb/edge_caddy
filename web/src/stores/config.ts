import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { http, errorText } from '@/api/http'
import type {
  DraftMeta,
  DraftsWire,
  Paged,
  PolicyWire,
  ResKind,
  RouteWire,
  RuleWire,
  RuleIssue,
  RulesWire,
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
  /**
   * 底下没有 live 资源的草稿。**永远打不开、也下发不了。**
   *
   * 草稿是在已有资源上的 Partial，没有底子合并不出东西（契约 §7.1）。
   * 主控会在预览时用 `field: "res_key"` 挡下它。
   */
  orphan?: boolean
}

export const useConfigStore = defineStore('config', () => {
  const routes = ref<RouteWire[]>([])
  const rules = ref<RuleWire[]>([])
  /**
   * `request_filter` 的 field → 允许的 op（契约 §6.2）。**后端报的，不是抄的。**
   *
   * `null` = 主控还没报（太旧）。那一档界面要说出来，
   * **不能退回一份本地默认表** —— 那正是这张表存在的理由要消灭的东西。
   */
  const filterFields = ref<Record<string, string[]> | null>(null)
  /** 哪些 field 还要指明「看哪一个」。同样从表里读，不再判一次 field 名。 */
  const filterNeedsName = ref<Record<string, boolean> | null>(null)

  /**
   * 每条规则还差什么（契约 §6.2）。`ruleId → 问题清单`。
   *
   * **`null` 有两个来源，处置相同**：主控太旧（整个字段没有），
   * 或者某一条这次算不出来。两者都不能显示成「这条是完整的」。
   */
  const ruleIssues = ref<Record<string, RuleIssue[] | null> | null>(null)
  const policies = ref<PolicyWire[]>([])
  const patches = ref<Record<string, Patch>>({})
  /**
   * 草稿**没能写到主控上**的那些资源，按 key 记原因。
   *
   * 与 `patches` 是两回事：`patches` 是本地有什么，这个是**主控上缺什么**。
   * 下发消费的是主控上的草稿（契约 §7.2：一次成功的下发把草稿删掉并合入 live），
   * 所以这里非空时，界面上那个「N 处未下发改动」正在虚报 —— 它数的是本地的。
   */
  const unsaved = ref<Record<string, string>>({})
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
        // 策略同样有 version 0（从没下发过）。这里原先写死 false，而工作台标题栏
        // 对同一条策略显示「尚未下发到任何节点」—— 同一个事实，两处不同答案，
        // 而人会以为树上没「新」就是已经下发过了。
        isNew: p.version === 0,
      })
    }
    /*
     * **底下没有 live 资源的草稿，也要在树上占一行。**
     *
     * 上面三个循环遍历的是 live 资源，`dirtyKeys` 来自草稿 —— 两者对不上时，
     * 那个 key 在树里根本不存在。而顶栏的「N 处变更」是按草稿数的：
     * **数字说有一处，列表里一处都找不到，人没法处置一个看不见的东西。**
     *
     * 这种草稿今天从控制台产生不出来（草稿的 key 全部来自已有资源），
     * 只有运维机器人写得进去。但「新建规则」这个功能一旦做出来，第一个撞上
     * 它的就是它 —— 所以先把它显示出来，而不是等到那时候。
     *
     * 不给它 `dirty` 标记：那个点的意思是「这份资源有未下发的改动」，
     * 而这里连资源都没有。它要说的是另一件事。
     */
    const known = new Set(items.map((i) => i.key))
    for (const key of dirtyKeys.value) {
      if (known.has(key)) continue
      items.push({
        key,
        kind: (key.split(':')[0] as ResKind) ?? 'route',
        label: key,
        group: '没有底子的草稿',
        dirty: false,
        changes: changesOf(key),
        isNew: false,
        orphan: true,
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
      http.get<RulesWire>('/rules'),
      http.get<PolicyWire>('/policies/tls'),
      http.get<PolicyWire>('/policies/log'),
      http.get<DraftsWire>('/drafts'),
    ])

    if (rt.status === 'fulfilled') routes.value = rt.value.items
    else failed.push('反代路由')

    if (rl.status === 'fulfilled') {
      rules.value = rl.value.items
      /*
       * **后端报出来的那张表，原样收着**（契约 §6.2）。
       *
       * 主控太旧时它是 undefined —— 那一档下拉没有东西可渲染，
       * 界面要说「这个主控还不支持」，而不是退回一份抄来的默认表：
       * 抄的那份正是这两张表存在的理由要消灭的东西。
       */
      filterFields.value = rl.value.filter_fields ?? null
      filterNeedsName.value = rl.value.filter_fields_need_name ?? null
      ruleIssues.value = rl.value.incomplete ?? null
    } else failed.push('访问规则')

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

  /**
   * 把一条草稿写回主控。
   *
   * 失败**不打断输入**（人正在打字，弹个错只会碍事），但必须记下来 ——
   * 这里原先写的是「下一次输入会重试，点下发前还会整体重取一次」，
   * **后半句是假的**：`runPreview` 拿的是本地的 `dirtyKeys`，后端用**它自己那份**
   * 草稿渲染。写没成功的话，顶栏照样显示「N 处未下发改动」，而下发出去的东西
   * 里根本没有那处改动 —— 人看着一个自己刚敲的数字，下发了一份不含它的配置。
   *
   * 一句自信的假理由比没有理由更糟：没有理由的地方人会去查，写着理由的地方
   * 人会放心（这条判据来自后端那次同样的自我修正）。
   */
  async function persist(key: string): Promise<void> {
    try {
      // Partial 为空对象时后端会删掉该草稿行，等价于「这个资源没有未下发改动」
      await http.put(`/drafts/${encodeURIComponent(key)}`, patches.value[key] ?? {})
      const e = { ...unsaved.value }
      delete e[key]
      unsaved.value = e
    } catch (e) {
      unsaved.value = { ...unsaved.value, [key]: errorText(e, '写回主控失败') }
    }
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
  function flush(opts: { keepalive?: boolean } = {}): Promise<void> {
    const keys = [...timers.keys()]
    const pending: Promise<void>[] = []
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
        pending.push(persist(k))
      }
    }
    // 返回 Promise 是为了让调用方（和测试）能等它落定。keepalive 那一支等不了 ——
    // 页面正在卸载，await 不会有机会执行完。
    return Promise.all(pending).then(() => undefined)
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
   * **这依赖后端当前的行为：`DELETE /rules/:id` 会连同这条规则的草稿一起清掉。**
   * 契约 §6.2 只写了删除本身，没写草稿 —— 所以这句话没有流程保护它，
   * 后端哪天改了不会有人来通知我。（判据来自后端：写下一个理由时问一句
   * 「它依赖的那个东西，改的时候会不会有人通知我」—— 契约会，ADR 会，
   * 对方的内部实现不会。换不掉出处的，就把保质期写进句子的语法里。）
   *
   * 本地跟着清是**无论如何都对的**：留一份指向已删资源的草稿，会让顶栏的
   * 「有几处未下发改动」算上一个再也下发不出去的东西。后端那边真变了，
   * 这里也只是多清一次。
   */
  /**
   * 新建一条访问规则。
   *
   * **走 `PUT /rules/:id`，不是 POST** —— 契约 §6.2 没有 `POST /rules`，
   * 那一句写在草稿那一节：「要新建资源，先把资源本身建出来
   * （`POST /routes`、`PUT /rules/:id`），再改它的草稿」。
   *
   * ## 建出来的是一条停用、未绑定的规则
   *
   * 后端的校验**只跑在启用且已绑定的规则上**，所以一条 `enabled: false` +
   * `apply_to: []` 的骨架存得进去，哪怕 spec 还是空的。人接着去工作台把它配好、
   * 绑上域名、再启用 —— 那是这三步该有的顺序。
   *
   * 反过来做（建的时候就要求填全）会把一个多字段表单塞进新建弹层，
   * 而工作台里已经有一份更好的了。
   *
   * ## `PUT` 是 upsert，所以重名会**覆盖**
   *
   * 这跟路由不同：`POST /routes` 遇到重名回 `1004`，而这里后端不会拒 ——
   * 它会把已有的那条整个换掉，**而且回 `code: 0`**。
   * 所以重名必须在调用这里之前拦住（见 `NewRuleModal`）。
   */
  async function createRule(id: string, body: Record<string, unknown>): Promise<void> {
    // **putIfAbsent，不是 put**：本地那份规则列表可能为空或陈旧，
    // 只靠它查重会静默覆盖别人配好的规则（issue #70）。
    await http.putIfAbsent(`/rules/${id}`, body)
    await fetchAll().catch(() => {})
  }

  async function deleteRule(id: string): Promise<void> {
    await http.del(`/rules/${id}`)
    const copy = { ...patches.value }
    delete copy[`rule:${id}`]
    patches.value = copy
    await fetchAll().catch(() => {})
  }

  /**
   * 下发成功后把已下发的那几条草稿从本地清掉。
   *
   * **它只做这一件事。** 注释原先写着「并把版本推进」—— 而函数体里没有任何
   * 一行动 version：那是靠调用点紧跟着的 `fetchAll()` 从主控重新取回来的。
   * 一句说得比做得多的注释，下一个人会照着它去别处找 bug。
   *
   * 为什么本地先清一次而不是只等 fetchAll：那一趟请求要时间，而顶栏的
   * 「N 处未下发改动」在那期间会继续显示已经下发掉的数字 —— 人刚点完下发，
   * 最不该看到的就是那个数字没动。
   *
   * 契约 §7.2 现在列了一次成功下发的三件副作用：草稿删掉且合入 live、
   * version +1、基线换成新 cfg_version 且各节点 drift 清零。**后两件只有主控
   * 知道**，所以必须重取，本地推不出来。
   */
  function commit(keys: string[]): void {
    const copy = { ...patches.value }
    for (const k of keys) delete copy[k]
    patches.value = copy
  }

  return {
    setRuleSecret,
    createRule,
    deleteRule,
    routes,
    rules,
    filterFields,
    filterNeedsName,
    ruleIssues,
    policies,
    patches,
    updated,
    loading,
    error,
    failedParts,
    unsaved,
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
    commit,
  }
})
