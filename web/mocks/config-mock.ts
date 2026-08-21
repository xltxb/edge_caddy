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
  if (m === 'GET' && path === '/api/v1/rules') return paged(res, state.rules), true

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
