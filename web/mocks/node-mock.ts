import type { IncomingMessage, ServerResponse } from 'node:http'
import type { LogLevel, NodeWire } from '../src/api/types'
import * as seed from './seed'

/**
 * 节点状态的 mock —— 同样在 **Node 侧**。
 *
 * 判断依据和 config-mock 一样：节点状态会被 `dns` / `drain` 改写、被心跳帧读、
 * `push` 还要经 WS 推进度。三者跨运行时就会各持一份副本。
 */

type Rec = Record<string, unknown>

export const nodeState = {
  nodes: seed.nodes.map((n) => ({ ...n, cpu_series: n.cpu_series ? [...n.cpu_series] : null })),
  logs: Object.fromEntries(
    Object.entries(seed.agentLogs).map(([k, v]) => [k, v.map((l) => ({ ...l }))]),
  ) as Record<string, { at: string; level: LogLevel; msg: string }[]>,
  /** 节点上 Caddy Admin 的可达性。与隧道可达性分开 —— 两种故障处置不同。 */
  caddyAdmin: Object.fromEntries(seed.nodes.map((n) => [n.id, n.status !== 'down'])) as Record<
    string,
    boolean
  >,
}

function log(nodeId: string, level: LogLevel, msg: string): void {
  const l = nodeState.logs[nodeId]
  if (l) l.unshift({ at: new Date().toISOString(), level, msg })
}

const json = (res: ServerResponse, body: unknown) => {
  res.statusCode = 200
  res.setHeader('Content-Type', 'application/json')
  res.end(JSON.stringify(body))
}
const ok = (res: ServerResponse, data: unknown) => json(res, { code: 0, data, msg: '' })
const failCode = (res: ServerResponse, code: number, msg: string) =>
  json(res, { code, data: null, msg })
const paged = (res: ServerResponse, items: unknown[]) => ok(res, { items, next_before_id: null })

async function readBody(req: IncomingMessage): Promise<Rec> {
  const chunks: Buffer[] = []
  for await (const c of req) chunks.push(c as Buffer)
  const raw = Buffer.concat(chunks).toString('utf8')
  return raw ? (JSON.parse(raw) as Rec) : {}
}

export interface NodeMockDeps {
  send: (frame: unknown) => void
  baseline: () => string
}

function pushEvent(deps: NodeMockDeps, node: string | null, kind: string, msg: string): void {
  deps.send({
    type: 'event',
    data: { id: Math.floor(Math.random() * 1e6), at: new Date().toISOString(), node, kind, msg },
  })
}

/** 返回 true 表示已处理。 */
export async function handleNodes(
  req: IncomingMessage,
  res: ServerResponse,
  deps: NodeMockDeps,
): Promise<boolean> {
  const path = (req.url ?? '').split('?')[0] ?? ''
  const m = req.method ?? 'GET'

  if (m === 'GET' && path === '/api/v1/nodes') {
    const baseline = deps.baseline()
    return (
      paged(
        res,
        nodeState.nodes.map((n) => ({ ...n, drift: n.cfg_version !== baseline })),
      ),
      true
    )
  }

  if (m === 'POST' && path === '/api/v1/nodes/token') {
    const b = await readBody(req)
    const token = `ec_${Math.random().toString(16).slice(2, 10)}`
    return (
      ok(res, {
        token,
        expires_at: new Date(Date.now() + 30 * 60_000).toISOString(),
        install_cmd: `curl -fsSL https://ec.internal/install.sh | sudo bash -s -- --token ${token} --master ec.internal:9000 --node-id ${String(b.node_id ?? '')}`,
      }),
      true
    )
  }

  const logs = /^\/api\/v1\/nodes\/([^/]+)\/logs$/.exec(path)
  if (m === 'GET' && logs) {
    return paged(res, nodeState.logs[decodeURIComponent(logs[1]!)] ?? []), true
  }

  const act = /^\/api\/v1\/nodes\/([^/]+)\/(push|dns|probe|drain)$/.exec(path)
  if (m === 'POST' && act) {
    const id = decodeURIComponent(act[1]!)
    const node = nodeState.nodes.find((n) => n.id === id)
    if (!node) return failCode(res, 1003, '找不到这个节点'), true

    if (act[2] === 'push') {
      // 对已下线节点重推是状态冲突，不是参数错
      if (node.status === 'down') return failCode(res, 2001, '节点已下线，无法重推配置'), true
      log(id, 'info', `config ${deps.baseline()} received`)
      pushEvent(deps, id, 'ok', `已向 ${id} 重推基线 ${deps.baseline()}`)
      return ok(res, { deploy_id: 0, cfg_version: deps.baseline() }), true
    }

    if (act[2] === 'dns') {
      const b = await readBody(req)
      node.dns_enabled = b.enabled === true
      log(id, 'warn', `dns weight ${node.dns_enabled ? 'restored' : 'set to 0'}`)
      pushEvent(
        deps,
        id,
        node.dns_enabled ? 'ok' : 'warn',
        `${id} ${node.dns_enabled ? '已恢复解析' : '已暂停解析'}，其余节点权重已重新归一化`,
      )
      return ok(res, { id, dns_enabled: node.dns_enabled, weights_rebalanced: true }), true
    }

    if (act[2] === 'probe') {
      if (node.status === 'down') return failCode(res, 3002, '探活超时，节点不可达'), true
      const rtt = 20 + Math.floor(Math.random() * 60)
      log(id, 'info', `probe ok · rtt ${rtt}ms`)
      return (
        ok(res, {
          reachable: true,
          rtt_ms: rtt,
          caddy_admin: nodeState.caddyAdmin[id] ?? true,
          cfg_version: node.cfg_version,
        }),
        true
      )
    }

    // drain
    const b = await readBody(req)
    if (b.confirm !== true) return failCode(res, 1001, '下线操作必须显式确认'), true
    node.dns_enabled = false
    node.status = 'down'
    node.conns = 0
    log(id, 'warn', 'tunnel closed by operator')
    pushEvent(deps, id, 'warn', `${id} 已下线：解析摘除、连接排空、隧道关闭`)
    return (
      ok(res, {
        steps: [
          { step: 'dns_removed', ok: true },
          { step: 'conns_drained', ok: true, detail: `等待 ${node.conns} 连接结束，耗时 8.2s` },
          { step: 'tunnel_closed', ok: true },
        ],
      }),
      true
    )
  }

  return false
}

/** 供 ws-plugin 发心跳用 —— 与 REST 读的是同一份状态。 */
export function heartbeatNodes(): NodeWire[] {
  return nodeState.nodes
}
