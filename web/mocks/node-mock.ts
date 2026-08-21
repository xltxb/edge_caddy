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

/** 各线路的权重配置。weight 是配置值，share 由 dns_enabled 实时算出来。 */
const LINE_NAMES: Record<string, string> = {
  ct: '电信',
  cu: '联通',
  cm: '移动',
  tw: '台湾',
  ov: '境外 / 默认',
}

function freshNodes() {
  return {
    nodes: seed.nodes.map((n) => ({ ...n, cpu_series: n.cpu_series ? [...n.cpu_series] : null })),
    logs: Object.fromEntries(
      Object.entries(seed.agentLogs).map(([k, v]) => [k, v.map((l) => ({ ...l }))]),
    ) as Record<string, { at: string; level: LogLevel; msg: string }[]>,
    /** 节点上 Caddy Admin 的可达性。与隧道可达性分开 —— 两种故障处置不同。 */
    caddyAdmin: Object.fromEntries(seed.nodes.map((n) => [n.id, n.status !== 'down'])) as Record<
      string,
      boolean
    >,
    weights: Object.fromEntries(
      seed.dnsLines.map((l) => [l.line_code, { ...l.weights }]),
    ) as Record<string, Record<string, number>>,
  }
}

export const nodeState = freshNodes()

/** 复位到 seed —— 只给 e2e 用。 */
export function resetNodes(): void {
  Object.assign(nodeState, freshNodes())
}

/**
 * 算出各线路的实际占比。
 *
 * 退出解析的节点 share 为 0，它的权重在该线路内的其余节点间**重新归一化** ——
 * 所以在命令面板 pause 一个节点，这一页的占比条会立刻重排。
 */
function buildLines() {
  return Object.entries(nodeState.weights).map(([code, weights]) => {
    const enabled = Object.entries(weights).filter(([id]) => {
      const n = nodeState.nodes.find((x) => x.id === id)
      return n?.dns_enabled === true
    })
    const total = enabled.reduce((s, [, w]) => s + w, 0)
    return {
      code,
      name: LINE_NAMES[code] ?? code,
      entries: Object.entries(weights).map(([id, weight]) => {
        const n = nodeState.nodes.find((x) => x.id === id)
        const on = n?.dns_enabled === true
        return {
          node: id,
          weight,
          share: on && total > 0 ? Math.round((weight / total) * 1000) / 10 : 0,
          dns_enabled: on,
          status: n?.status ?? 'down',
        }
      }),
    }
  })
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
/**
 * mock 里当作同步过一次。
 *
 * 真主控在从没同步过时给 `null`（契约 §0.4）。mock 这边给真实时刻，
 * 「没有时刻」那条路径在单测里覆盖，不靠 mock。
 */
function dnsSync() {
  return { ok: true, at: new Date().toISOString(), detail: '解析安排已同步到服务商' }
}

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
      ok(res, {
        items: nodeState.nodes.map((n) => ({ ...n, drift: n.cfg_version !== baseline })),
        next_before_id: null,
        baseline,
        dns_sync: dnsSync(),
      }),
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

  const act = /^\/api\/v1\/nodes\/([^/]+)\/(push|dns|probe|drain|rejoin)$/.exec(path)
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

    if (act[2] === 'rejoin') {
      // 解析**不**跟着打开：能接入不等于该马上分流量，它刚回来，配置可能还是旧的
      node.drained_at = null
      log(id, 'info', 'rejoin allowed by operator')
      pushEvent(deps, id, 'ok', `${id} 已重新上线，解析仍是关闭的`)
      return (
        ok(res, {
          id,
          drained_at: null,
          dns_enabled: node.dns_enabled === true,
          detail: '已允许重新接入；解析仍是关闭的，确认配置无误后再打开',
        }),
        true
      )
    }

    if (act[2] === 'dns') {
      const b = await readBody(req)
      // 已下线的节点开解析要拒；关不拒
      if (b.enabled === true && node.drained_at) {
        return failCode(res, 2001, '该节点已被下线，先「重新上线」再恢复解析'), true
      }
      node.dns_enabled = b.enabled === true
      log(id, 'warn', `dns weight ${node.dns_enabled ? 'restored' : 'set to 0'}`)
      pushEvent(
        deps,
        id,
        node.dns_enabled ? 'ok' : 'warn',
        `${id} ${node.dns_enabled ? '已恢复解析' : '已暂停解析'}，其余节点权重已重新归一化`,
      )
      return (
        ok(res, {
          id,
          dns_enabled: node.dns_enabled,
          // mock 里当作已配好服务商 —— 但 detail 照样发，免得前端只在
          // 「没同步」那条路径上才拿得到文案，而那条路径 mock 里走不到。
          dns_synced: true,
          detail: '解析安排已同步到服务商',
        }),
        true
      )
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
    const before = Number(node.conns ?? 0)
    node.dns_enabled = false
    // **不动 status。** status 是观察（有没有心跳），drained_at 是意图。
    // mock 早先在这里写 status='down'，那正是被 ADR-0014 拆开的那个混淆 ——
    // 而且它会让「已下线且在线」这个真实存在的组合在 mock 里永远出不来。
    node.drained_at = new Date().toISOString()
    node.conns = 0
    log(id, 'warn', 'tunnel closed by operator')
    pushEvent(deps, id, 'warn', `${id} 已下线：解析摘除、连接排空、隧道关闭`)
    return (
      ok(res, {
        steps: [
          { step: 'dns_removed', ok: true, detail: '解析安排已同步到服务商' },
          {
            step: 'conns_drained',
            ok: true,
            // 这句边界不能省：DNS 有 TTL，「已排空」说的是回报那一刻的连接数
            detail: `已建立的 ${before} 条连接都已结束；解析缓存未过期前仍可能有新连接进来`,
          },
          { step: 'tunnel_closed', ok: true, detail: '隧道已断开，此后拒绝该节点重连' },
        ],
      }),
      true
    )
  }

  return false
}

/** DNS 权重。与节点状态同侧，因为 share 要按 dns_enabled 实时归一化。 */
export async function handleDns(req: IncomingMessage, res: ServerResponse): Promise<boolean> {
  const path = (req.url ?? '').split('?')[0] ?? ''
  if (path !== '/api/v1/dns/weights') return false

  if (req.method === 'GET') {
    // mock 里用 cloudflare，因为它是**能力更少**的那家 —— 拿能力多的那家开发，
    // 界面上那条限制就永远走不到。
    return (
      ok(res, {
        domain: 'cdn.example.com',
        dns_sync: dnsSync(),
        lines: buildLines(),
        capabilities: {
          kind: 'cloudflare',
          // covers 由服务商给：前端不持有任何服务商的地理模型
          lines: [
            { code: 'cn', name: '中国（电信 / 联通 / 移动合并）', covers: ['ct', 'cu', 'cm'] },
            { code: 'tw', name: '台湾', covers: ['tw'] },
            { code: 'ov', name: '境外 / 默认', covers: ['ov'] },
          ],
          weights: true,
          notes:
            'Cloudflare 的 DNS 记录没有权重与线路概念，加权调度走 Load Balancing，' +
            '其地理维度是国家 / 大洲：电信 / 联通 / 移动无法区分，三者会被合并为「中国」。',
        },
      }),
      true
    )
  }

  if (req.method === 'PUT') {
    const b = (await readBody(req)) as { lines?: { code: string; entries: { node: string; weight: number }[] }[] }
    for (const l of b.lines ?? []) {
      const bucket = nodeState.weights[l.code]
      if (!bucket) continue
      for (const e of l.entries) bucket[e.node] = Math.max(0, Math.round(e.weight))
    }
    return ok(res, { lines: buildLines() }), true
  }
  return false
}

/** 供 ws-plugin 发心跳用 —— 与 REST 读的是同一份状态。 */
export function heartbeatNodes(): NodeWire[] {
  return nodeState.nodes
}
