import type { Plugin, ViteDevServer } from 'vite'
import { WebSocketServer, type WebSocket } from 'ws'
import type { EventKind } from '../src/api/types'
import { handleConfig, resetConfig } from './config-mock'
import { handleDeploy, resetDeploys } from './deploy-mock'
import { handleDns, handleNodes, heartbeatNodes, resetNodes, setSyncOverride } from './node-mock'
import * as seed from './seed'

const WS_PATH = '/api/v1/ws'
const HEARTBEAT_MS = 3_000
const EVENT_MS = 3_800

/** 在 [-d, +d] 内抖动并夹到 [0, 100]，让 sparkline 看起来是活的。 */
function jitter(base: number, d: number): number {
  const v = base + (Math.random() * 2 - 1) * d
  return Math.round(Math.max(0, Math.min(100, v)) * 10) / 10
}

const ROLLING_EVENTS: { node: string | null; msg: string; kind: EventKind }[] = [
  { node: 'node-kr-01', msg: 'CPU 持续高于 80%，建议扩容或分流', kind: 'warn' },
  { node: 'node-hk-01', msg: '回源 10.8.0.2:8080 rtt 41ms', kind: 'info' },
  { node: 'node-tw-01', msg: 'gRPC 隧道重连中…', kind: 'warn' },
  { node: 'node-de-01', msg: 'abort 2 个来自 8.210.x.x 的请求', kind: 'info' },
  { node: null, msg: `当前基线 ${seed.BASELINE}，6 个节点中 4 个版本一致`, kind: 'info' },
]

/**
 * dev 下的实时通道 mock —— 起一个**真** WebSocket 服务端，不是 stub。
 *
 * 这样 src/api/ws.ts 的重连退避与「实时已断 → 2s 轮询」降级是被真实走到的：
 * 停掉 dev server，前端应当先 reconnecting 再转 polling 并在顶栏说出来。
 * 换成内存里的假 socket 就永远测不到那条路径。
 */
export function wsMockPlugin(): Plugin {
  return {
    name: 'edge-ws-mock',
    apply: 'serve',
    configureServer(server: ViteDevServer) {
      const wss = new WebSocketServer({ noServer: true })
      const clients = new Set<WebSocket>()

      server.httpServer?.on('upgrade', (req, socket, head) => {
        if (!req.url?.startsWith(WS_PATH)) return // 让 Vite 自己的 HMR socket 过去
        wss.handleUpgrade(req, socket, head, (ws) => {
          clients.add(ws)
          ws.on('close', () => clients.delete(ws))
          wss.emit('connection', ws, req)
        })
      })

      // 下发端点由 Node 侧处理，这样进度可以真的经 WS 推回去（见 deploy-mock.ts）。
      // MSW 的 onUnhandledRequest 是 bypass，所以浏览器端不拦这几条，请求会走到这里。
      // 会被下发改到的那些状态（配置资源、草稿、下发本身）统一在 Node 侧，
      // 与 ws 同进程 —— 否则下发消费掉的草稿，浏览器侧的 MSW 并不知道。
      const MUTABLE = [
        '/api/v1/deploys',
        '/api/v1/drafts',
        '/api/v1/routes',
        '/api/v1/rules',
        '/api/v1/policies',
        '/api/v1/nodes',
        '/api/v1/dns',
      ]
      /*
       * 仅 mock 存在的复位端点，给 e2e 用。
       *
       * mock 的状态会被下发改掉（草稿被消费、version 递增），用例之间会互相
       * 影响。让每个用例自己复位，比让它们小心地共享一份漂移的状态可靠得多。
       * 这个端点不在契约里，真主控上不存在 —— e2e 只跑 mock 模式。
       */
      /*
       * **e2e 造同步场景用**，与 `__test/reset` 同类：不在契约里，真主控上不存在。
       * `/dns/weights` 由这个插件提供，playwright 的 `page.route` 拦不到它。
       */
      server.middlewares.use((req, res, next) => {
        if (req.url !== '/api/v1/__test/dns-sync' || req.method !== 'POST') return next()
        let raw = ''
        req.on('data', (c) => (raw += c))
        req.on('end', () => {
          setSyncOverride(raw ? JSON.parse(raw) : null)
          res.statusCode = 200
          res.setHeader('Content-Type', 'application/json')
          res.end(JSON.stringify({ code: 0, data: null, msg: '' }))
        })
      })

      server.middlewares.use((req, res, next) => {
        if (req.url !== '/api/v1/__test/reset' || req.method !== 'POST') return next()
        resetConfig()
        resetNodes()
        resetDeploys()
        res.statusCode = 200
        res.setHeader('Content-Type', 'application/json')
        res.end(JSON.stringify({ code: 0, data: null, msg: '' }))
      })

      server.middlewares.use((req, res, next) => {
        const url = req.url ?? ''
        if (!MUTABLE.some((p) => url.startsWith(p))) return next()
        const run = url.startsWith('/api/v1/deploys')
          ? handleDeploy(req, res, { send })
          : url.startsWith('/api/v1/nodes')
            ? handleNodes(req, res, { send, baseline: () => seed.BASELINE })
            : url.startsWith('/api/v1/dns')
              ? handleDns(req, res)
              : handleConfig(req, res)
        run.then(
          (handled) => {
            if (!handled) next()
          },
          () => next(),
        )
      })

      /*
       * **最后一道：跟真主控一样，这三类不回落到 index.html。**
       *
       * Vite 的 SPA fallback 对任何未匹配路径都回 index，而主控的
       * `internal/api/web.go` 分得很细（后端 `web_test.go` 守着）：
       *
       *   - `/api/...` 与 `/ws` 永不回落，直接 404
       *   - `/assets/` 下找不到的文件直接 404 —— 回落会让一个 hash 变了的
       *     chunk 返回 HTML，浏览器报「Unexpected token '<'」，而那句话
       *     完全指不到真因
       *   - 其余路径回落 index（SPA 路由）
       *
       * 三条量过都不一致，方向都是 dev 更宽松。而「更宽松」在 `/api/` 这条上
       * 不是无害的：它把**「这个端点不存在」变成「返回了一段 HTML」**，
       * 于是同一个 bug 在 dev 和生产上给出两种完全不同的症状。
       * `GET /nodes/:id/logs` 那次就是这样 —— 真主控 404，而 dev 下它会拿到
       * 一份 index.html。
       *
       * 这一段放在所有 mock 中间件**之后**：MSW 拦掉的请求根本不会到服务端，
       * 上面那些 MUTABLE 路径也已经处理过了，落到这里的就是「两边都没实现」。
       */
      server.middlewares.use((req, res, next) => {
        const url = req.url ?? ''
        const p = url.split('?')[0] ?? ''
        const blocked =
          p.startsWith('/api/') || p === '/api' || p === '/ws' || p.startsWith('/assets/')
        if (!blocked) return next()
        res.statusCode = 404
        res.setHeader('Content-Type', 'text/plain; charset=utf-8')
        res.end(`404 —— 主控不会把 ${p} 回落到 index.html，dev 这里也不。\n`)
      })

      const send = (frame: unknown) => {
        const text = JSON.stringify(frame)
        for (const ws of clients) {
          if (ws.readyState === ws.OPEN) ws.send(text)
        }
      }

      const hb = setInterval(() => {
        // 与 REST 读同一份状态 —— 暂停解析 / 下线之后心跳要跟着变
        for (const n of heartbeatNodes()) {
          if (n.status === 'down') continue // 离线节点本来就不该有心跳
          send({
            type: 'heartbeat',
            data: {
              id: n.id,
              status: n.status,
              cpu: jitter(n.cpu, 4),
              mem: jitter(n.mem, 2),
              conns: Math.max(0, Math.round(n.conns * (0.96 + Math.random() * 0.08))),
              // 刚到达的帧里 hb_age_ms 接近 0，前端从这里开始本地计时
              hb_age_ms: Math.round(Math.random() * 80),
              cfg_version: n.cfg_version,
              routes: n.routes,
              rules: n.rules,
            },
          })
        }
      }, HEARTBEAT_MS)

      let i = 0
      let seq = 5000
      const ev = setInterval(() => {
        const e = ROLLING_EVENTS[i % ROLLING_EVENTS.length]!
        i += 1
        seq += 1
        send({ type: 'event', data: { id: seq, at: new Date().toISOString(), ...e } })
      }, EVENT_MS)

      server.httpServer?.on('close', () => {
        clearInterval(hb)
        clearInterval(ev)
        wss.close()
      })
    },
  }
}
