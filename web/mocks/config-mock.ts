import type { IncomingMessage, ServerResponse } from 'node:http'
import * as seed from './seed'

/**
 * 配置资源 + 草稿的 mock —— 与 deploy-mock 一样跑在 **Node 侧**。
 *
 * 为什么不放 MSW：草稿、配置资源、下发是**一簇互相耦合的可变状态**。
 * 一次下发会消费草稿、把改动并进 live、推进 version。把它们劈在两个运行时里
 * （MSW 在浏览器、ws mock 在 Node），两边各持一份 seed 副本，下发之后
 * 浏览器那边看到的就是过期状态 —— 表现为「下发完了草稿还在」。
 *
 * 判断依据很简单：**谁会被下发改到，谁就得和下发住在同一侧。**
 * 只读的那些（节点、证书、审计、设置）留在 MSW，那边写起来更省事。
 */

type Rec = Record<string, unknown>

function freshConfig() {
  return {
    routes: seed.routes.map((r) => ({ ...r })) as Rec[],
    rules: seed.rules.map((r) => ({ ...r, spec: { ...r.spec } })) as Rec[],
    policies: seed.policies.map((p) => ({ ...p, spec: { ...p.spec } })) as Rec[],
    drafts: { ...seed.draftItems } as Record<string, Rec>,
    draftMeta: { ...seed.draftUpdated } as Record<string, { by: string; at: string }>,
  }
}

/** 可变的 live 副本。seed 本身保持不可变。 */
export const state = freshConfig()

/** 复位到 seed —— 只给 e2e 用（见 ws-plugin 里的 __test/reset）。 */
export function resetConfig(): void {
  Object.assign(state, freshConfig())
}

function isPlainObject(v: unknown): v is Rec {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

export function findLive(key: string): Rec | undefined {
  const i = key.indexOf(':')
  const kind = key.slice(0, i)
  const id = key.slice(i + 1)
  if (kind === 'route') return state.routes.find((r) => r.domain === id)
  if (kind === 'rule') return state.rules.find((r) => r.id === id)
  return state.policies.find((p) => p.id === id)
}

/** live + 草稿。顶层浅合并，spec 再合一层——与前端 merge 的语义保持一致。 */
export function effective(key: string): Rec | undefined {
  const base = findLive(key)
  if (!base) return undefined
  const patch = state.drafts[key]
  if (!patch) return base
  const out: Rec = { ...base }
  for (const [k, v] of Object.entries(patch)) {
    const cur = out[k]
    out[k] = isPlainObject(cur) && isPlainObject(v) ? { ...cur, ...v } : v
  }
  return out
}

/** 下发落定后：把草稿并进 live、version+1、清掉这条草稿。 */
export function applyToLive(keys: string[]): void {
  for (const key of keys) {
    const base = findLive(key)
    const patch = state.drafts[key]
    if (!base || !patch) continue
    for (const [k, v] of Object.entries(patch)) {
      const cur = base[k]
      base[k] = isPlainObject(cur) && isPlainObject(v) ? { ...cur, ...v } : v
    }
    base.version = (typeof base.version === 'number' ? base.version : 0) + 1
    delete state.drafts[key]
    delete state.draftMeta[key]
  }
}

const json = (res: ServerResponse, body: unknown) => {
  res.statusCode = 200
  res.setHeader('Content-Type', 'application/json')
  res.end(JSON.stringify(body))
}
const ok = (res: ServerResponse, data: unknown) => json(res, { code: 0, data, msg: '' })
const paged = (res: ServerResponse, items: unknown[]) => ok(res, { items, next_before_id: null })

async function readBody(req: IncomingMessage): Promise<Rec> {
  const chunks: Buffer[] = []
  for await (const c of req) chunks.push(c as Buffer)
  const raw = Buffer.concat(chunks).toString('utf8')
  return raw ? (JSON.parse(raw) as Rec) : {}
}

/** 返回 true 表示已处理。 */
export async function handleConfig(req: IncomingMessage, res: ServerResponse): Promise<boolean> {
  const path = (req.url ?? '').split('?')[0] ?? ''
  const m = req.method ?? 'GET'

  if (m === 'GET' && path === '/api/v1/routes') return paged(res, state.routes), true

  if (m === 'POST' && path === '/api/v1/routes') {
    const b = await readBody(req)
    const domain = String(b.domain ?? '')
    // 重名返回 1004（契约 §6.1）—— 与「参数格式错」1001 分开，前端要落到域名框上
    if (state.routes.some((r) => r.domain === domain)) {
      return json(res, { code: 1004, data: null, msg: '这个域名已经有一条路由了' }), true
    }
    // 新建的 version 为 0：尚未下发到任何节点
    state.routes.push({ ...b, domain, version: 0 })
    return ok(res, { domain }), true
  }
  if (m === 'GET' && path === '/api/v1/rules') {
    /*
     * **这两张表是替身，真值以主控为准**（契约 §6.2）。
     *
     * 后端那份与它的校验共用同一张表（`model.FilterFieldOps`），
     * 界面照报出来的渲染、不抄 —— 而 mock 这一份**就是抄的**，
     * 因为替身除了抄没有别的办法。
     *
     * 两件事让这份抄本比一般的抄本更危险，都写出来：
     *
     * 1. **`check:shapes` 比不到它。** 那个脚本装的是进程内 MSW，
     *    而 `/rules` 只由这个 Node 侧插件提供 —— MSW 里根本没有这个端点。
     *    所以这份抄本跟真主控分叉时，没有任何一处会红。
     * 2. **探针也没验到。** 试过一次，主控进程比后端那次提交旧，
     *    `data` 顶层只有 `items`。所以下面这几行是照契约的示例
     *    加推断写的，**不是观测到的**。
     *
     * 分叉的两个方向不对称：给出一个后端会拒的 op，人配完被拒、还看得见；
     * **藏起一个后端接受的，从界面上完全看不出来**。后者更贵。
     * 这份抄本要是少了某个 op，dev 里就是后一种。
     */
    return (
      ok(res, {
        items: state.rules,
        next_before_id: null,
        filter_fields: {
          /*
           * **这张表照真主控抄，不猜。**
           *
           * 第一版是猜的，四处都错 —— `check:shapes` 改成打真 dev server 之后
           * 第一次跑就抓到了它们，而错的方向正是那条注释预言的那个：
           *
           * - `user_agent` / `header` 各少了 `prefix` / `suffix`
           * - `referer` 整个漏了
           * - `method` 是我凭空加的，真主控根本没有这个 field
           *
           * 前三条都是**藏起一个后端接受的**：dev 里那个下拉少几个选项，
           * 而界面上看不出任何异常 —— 人只会以为「就只有这些」。
           * 最后一条是相反方向：给出一个后端会拒的，人配完被拒，还算看得见。
           */
          header: ['contains', 'prefix', 'suffix', 'equals', 'regex'],
          path: ['contains', 'prefix', 'suffix', 'equals', 'regex'],
          // Caddy 的 query 匹配器只比精确值 —— 这一档只有 equals（契约 §6.2）
          query: ['equals'],
          referer: ['contains', 'prefix', 'suffix', 'equals', 'regex'],
          user_agent: ['contains', 'prefix', 'suffix', 'equals', 'regex'],
        },
        filter_fields_need_name: { header: true, query: true },
      }),
      true
    )
  }

  const rule = /^\/api\/v1\/rules\/([^/]+)$/.exec(path)
  if (m === 'DELETE' && rule) {
    const id = decodeURIComponent(rule[1]!)
    const i = state.rules.findIndex((r) => r.id === id)
    // 删不存在的要报 1003，不能假装成功 —— 假装成功会让「删掉了」和
    // 「压根没有过」在界面上长得一模一样
    if (i < 0) return json(res, { code: 1003, data: null, msg: '没有这条访问规则' }), true
    state.rules.splice(i, 1)
    // 草稿一并清掉：留一份指向已删资源的草稿，会让「有几处未下发改动」
    // 算上一个再也下发不出去的东西
    delete state.drafts[`rule:${id}`]
    delete state.draftMeta[`rule:${id}`]
    return ok(res, { deleted: id }), true
  }

  const pol = /^\/api\/v1\/policies\/(tls|log)$/.exec(path)
  if (m === 'GET' && pol) {
    const p = state.policies.find((x) => x.id === pol[1])
    if (!p) return json(res, { code: 1003, data: null, msg: '找不到这条全局策略' }), true
    return ok(res, p), true
  }

  if (m === 'GET' && path === '/api/v1/drafts') {
    return ok(res, { items: state.drafts, updated: state.draftMeta }), true
  }

  const draft = /^\/api\/v1\/drafts\/(.+)$/.exec(path)
  if (m === 'PUT' && draft) {
    const key = decodeURIComponent(draft[1]!)
    const patch = await readBody(req)
    // Partial 为空对象时删掉该草稿行 —— 等价于「这个资源没有未下发改动」
    if (Object.keys(patch).length === 0) {
      delete state.drafts[key]
      delete state.draftMeta[key]
    } else {
      state.drafts[key] = patch
      state.draftMeta[key] = { by: 'abiu', at: new Date().toISOString() }
    }
    return ok(res, null), true
  }

  if (m === 'DELETE' && path === '/api/v1/drafts') {
    state.drafts = {}
    state.draftMeta = {}
    return ok(res, null), true
  }

  return false
}
