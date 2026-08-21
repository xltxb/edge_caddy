import type { IncomingMessage, ServerResponse } from 'node:http'
import type { DeployProgressState } from '../src/api/types'
import { applyToLive, effective, findLive, state } from './config-mock'
import * as seed from './seed'

/**
 * 下发端点的 mock —— 跑在 **Node 侧**（Vite 插件里），不在 MSW 里。
 *
 * 原因是进度必须经 WebSocket 推回来。MSW 活在浏览器里，拦下请求后没有办法
 * 让 ws 服务端发帧；放在这里，mock 模式下走的就是**真** WS 那条路——
 * 逐节点进度、断线降级、retrying 语义全都被真实地走一遍，而不是绕过去。
 */

interface Progress {
  node: string
  state: DeployProgressState
  detail: string
  retrying: boolean
}

interface Running {
  id: number
  cfgVersion: string
  resKeys: string[]
  results: Map<string, Progress>
  phase: 'running' | 'done'
}

const running = new Map<number, Running>()
let nextId = 90

/**
 * 已排定但还没跑的进度回调。
 *
 * 下发进度是用 setTimeout 排的，最长在响应后 2.6 秒才落定并消费草稿。
 * 复位时不取消它们的话，上一个用例的下发会在下一个用例复位**之后**落定，
 * 把刚恢复的草稿又删一次 —— 表现为「单独跑绿、一起跑红」，而那种失败
 * 查起来最费时间。**一个不取消已排定工作的复位，不算复位。**
 */
const pending = new Set<ReturnType<typeof setTimeout>>()

function later(fn: () => void, ms: number): void {
  const t = setTimeout(() => {
    pending.delete(t)
    fn()
  }, ms)
  pending.add(t)
}

/** 复位 —— 只给 e2e 用。 */
export function resetDeploys(): void {
  for (const t of pending) clearTimeout(t)
  pending.clear()
  running.clear()
  nextId = 90
}

function nextCfg(): string {
  const hex = '0123456789abcdef'
  let s = ''
  for (let i = 0; i < 6; i++) s += hex[Math.floor(Math.random() * 16)]
  return `cfg-${s}`
}

/**
 * 渲染一份「全量配置」文本，充当后端的权威渲染。
 *
 * `withDrafts=false` 是基线，`true` 是叠加草稿后的结果 —— 确认弹层的
 * before / after 就是这两份。真后端渲染的是 Caddy JSON，这里形状是简化的，
 * 但**两份都由同一侧渲染**这个性质是一样的，diff 的权威性正来自于此。
 */
function renderAll(withDrafts: boolean): string {
  const pick = (key: string, base: Record<string, unknown>) =>
    withDrafts ? (effective(key) ?? base) : base

  return JSON.stringify(
    {
      apps: {
        http: {
          servers: {
            edge: {
              listen: [':443'],
              routes: state.routes.map((r) => pick(`route:${String(r.domain)}`, r)),
            },
          },
        },
      },
      access_rules: state.rules.map((r) => pick(`rule:${String(r.id)}`, r)),
      policies: state.policies.map((p) => pick(`global:${String(p.id)}`, p)),
    },
    null,
    2,
  )
}

const json = (res: ServerResponse, body: unknown, status = 200) => {
  res.statusCode = status
  res.setHeader('Content-Type', 'application/json')
  res.end(JSON.stringify(body))
}
const ok = (res: ServerResponse, data: unknown) => json(res, { code: 0, data, msg: '' })

async function readBody(req: IncomingMessage): Promise<Record<string, unknown>> {
  const chunks: Buffer[] = []
  for await (const c of req) chunks.push(c as Buffer)
  const raw = Buffer.concat(chunks).toString('utf8')
  return raw ? (JSON.parse(raw) as Record<string, unknown>) : {}
}

export interface DeployMockDeps {
  /** 往所有 WS 客户端广播一帧。 */
  send: (frame: unknown) => void
}

/**
 * 逐节点推进度。
 *
 * 刻意复现两条 ADR-0005 的分支：node-tw-01 超时（`retrying: true`，后面还有帧），
 * node-us-01 离线不可达（同样重试）。其余节点报耗时成功。
 *
 * 注意 `state: 'ok'` 的含义是「**Caddy 接受了这份配置**」，不是「流量正在被服务」——
 * 实测过端口被占用时 Caddy 会返回 200 且日志无 error，但流量进不来。
 */
function runProgress(run: Running, deps: DeployMockDeps): void {
  const nodes = seed.nodes.map((n) => n.id)
  const frame = (p: Progress) => {
    run.results.set(p.node, p)
    deps.send({
      type: 'deploy_progress',
      data: { deploy_id: run.id, cfg_version: run.cfgVersion, ...p },
    })
  }

  for (const id of nodes) frame({ node: id, state: 'wait', detail: '待下发', retrying: false })

  nodes.forEach((id, i) => {
    const at = 300 + i * 420
    later(() => frame({ node: id, state: 'run', detail: '热重载中', retrying: false }), at)
    later(() => {
      if (id === 'node-tw-01') {
        frame({ node: id, state: 'fail', detail: 'deadline exceeded', retrying: true })
      } else if (id === 'node-us-01') {
        frame({ node: id, state: 'fail', detail: '节点不可达', retrying: true })
      } else {
        frame({ node: id, state: 'ok', detail: `${28 + Math.floor(Math.random() * 25)}ms`, retrying: false })
      }
      if (i === nodes.length - 1) {
        // 重试要有归宿，否则那两行会永远停在「重试中」，弹层永远不落定。
        // 真后端会继续发帧（ADR-0005：只重试传输层失败），这里复现两种结局：
        // 台北重连成功，洛杉矶依然不可达且**停止重试**（转为终态，等人处理）。
        later(() => {
          frame({ node: 'node-tw-01', state: 'ok', detail: '47ms（重试第 2 次）', retrying: false })
        }, 1400)
        later(() => {
          frame({ node: 'node-us-01', state: 'fail', detail: '节点不可达 · 已放弃重试', retrying: false })
          settle(run, deps, nodes.length)
        }, 2600)
        return
      }
    }, at + 600)
  })
}

function settle(run: Running, deps: DeployMockDeps, total: number): void {
  run.phase = 'done'
  // 只带**本次勾选**的草稿：并进 live、version+1、清掉草稿。未勾选的仍是草稿。
  applyToLive(run.resKeys)
  const okN = [...run.results.values()].filter((r) => r.state === 'ok').length
  deps.send({
    type: 'event',
    data: {
      id: Date.now() % 100000,
      at: new Date().toISOString(),
      node: null,
      kind: okN === total ? 'ok' : 'warn',
      msg: `配置 ${run.cfgVersion} 下发完成，${okN}/${total} 个节点接受了配置`,
    },
  })
}

/** 返回 true 表示这个请求已被处理。 */
export async function handleDeploy(
  req: IncomingMessage,
  res: ServerResponse,
  deps: DeployMockDeps,
): Promise<boolean> {
  const url = req.url ?? ''
  const path = url.split('?')[0] ?? ''

  if (req.method === 'POST' && path === '/api/v1/deploys/preview') {
    const body = await readBody(req)
    const resKeys = (body.res_keys as string[]) ?? []
    // 校验失败在预览里返回 code: 0 —— 预览成功地告诉了你「校验没过」（契约 §7.1）
    // 没有 cfg_version：新版本号在 POST /deploys 那一刻才生成，
    // 预览时给一个必然对不上的号，就是在弹层和下发记录之间埋一处不一致。
    ok(res, {
      before: renderAll(false),
      after: renderAll(true),
      baseline: seed.BASELINE,
      targets: seed.nodes.map((n) => ({ id: n.id, status: n.status })),
      validation: { ok: true, errors: [] },
      _res_keys: resKeys,
    })
    return true
  }

  if (req.method === 'POST' && path === '/api/v1/deploys') {
    const body = await readBody(req)
    const resKeys = (body.res_keys as string[]) ?? []
    const run: Running = {
      id: ++nextId,
      cfgVersion: nextCfg(),
      resKeys,
      results: new Map(),
      phase: 'running',
    }
    running.set(run.id, run)
    ok(res, { deploy_id: run.id, cfg_version: run.cfgVersion, targets: seed.nodes.map((n) => n.id) })
    later(() => runProgress(run, deps), 120)
    return true
  }

  const detail = /^\/api\/v1\/deploys\/(\d+)$/.exec(path)
  if (req.method === 'GET' && detail) {
    const run = running.get(Number(detail[1]))
    if (!run) {
      // 不是进行中的那次，就当历史记录查。这条也必须由 Node 侧回答：
      // MSW 活在浏览器里、先于网络拦截，交给它的话请求永远到不了这里。
      const hist = seed.deploys.find((d) => d.id === Number(detail[1]))
      if (!hist) {
        json(res, { code: 1003, data: null, msg: '找不到这次下发记录' })
        return true
      }
      ok(res, hist)
      return true
    }
    const results = [...run.results.values()]
    ok(res, {
      id: run.id,
      cfg_version: run.cfgVersion,
      operator: 'abiu',
      res_keys: run.resKeys,
      ok_count: results.filter((r) => r.state === 'ok').length,
      fail_count: results.filter((r) => r.state === 'fail').length,
      is_baseline: run.phase === 'done',
      created_at: new Date().toISOString(),
      targets: seed.nodes.map((n) => n.id),
      target_count: seed.nodes.length,
      phase: run.phase,
      results,
    })
    return true
  }

  const rb = /^\/api\/v1\/deploys\/([^/]+)\/rollback$/.exec(path)
  if (req.method === 'POST' && rb) {
    const cfg = decodeURIComponent(rb[1]!)
    const hist = seed.deploys.find((d) => d.cfg_version === cfg)
    if (!hist) return json(res, { code: 1003, data: null, msg: '找不到这个版本' }), true
    if (hist.is_baseline) {
      return json(res, { code: 2001, data: null, msg: '这是当前基线，无需回滚' }), true
    }
    // 回滚**不直接下发**：把差异写回草稿，由人在工作台确认后走同一条流水线。
    // mock 里简化成「把该次下发涉及的资源各造一处改动」，形状与真实一致。
    for (const key of hist.res_keys) {
      const live = findLive(key)
      if (!live) continue
      state.drafts[key] = { body_max: '8MB' }
      state.draftMeta[key] = { by: 'abiu', at: new Date().toISOString() }
    }
    // skipped：回滚覆盖不到的资源。mock 里造一条「之后才新建的」来走通这条分支。
    ok(res, {
      res_keys: hist.res_keys,
      skipped: [
        {
          res_key: 'route:push.example.com',
          reason: '这条路由是那次下发之后才新建的，回滚不会删除它',
        },
      ],
    })
    return true
  }

  return false
}
