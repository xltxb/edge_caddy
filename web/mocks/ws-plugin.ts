import type { Plugin, ViteDevServer } from 'vite'
import { WebSocketServer, type WebSocket } from 'ws'
import type { EventKind } from '../src/api/types'
import { handleConfig } from './config-mock'
import { handleDeploy } from './deploy-mock'
import { handleNodes, heartbeatNodes } from './node-mock'
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
      ]
      server.middlewares.use((req, res, next) => {
        const url = req.url ?? ''
        if (!MUTABLE.some((p) => url.startsWith(p))) return next()
        const run = url.startsWith('/api/v1/deploys')
          ? handleDeploy(req, res, { send })
          : url.startsWith('/api/v1/nodes')
            ? handleNodes(req, res, { send, baseline: () => seed.BASELINE })
            : handleConfig(req, res)
        run.then(
          (handled) => {
            if (!handled) next()
          },
          () => next(),
        )
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
