import type { Frame } from './types'

/**
 * connectWS 连上实时通道并把帧交给 onFrame，断线后指数退避重连。
 *
 * 退避而不是固定间隔重连：主控重启时所有节点与所有浏览器页签会同时断开，
 * 固定间隔会让它们在同一时刻一起重连，把刚起来的主控再打一次。
 *
 * 首切片的主控还没有 /ws 端点（属工单 #9），连不上是预期的——因此连接
 * 失败只退避重试，不向界面报错。
 */
export function connectWS(onFrame: (f: Frame) => void): () => void {
  const path = import.meta.env.VITE_WS_PATH ?? '/api/v1/ws'
  const backoff = [0, 1000, 2000, 4000, 8000, 15000]
  let attempt = 0
  let ws: WebSocket | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let stopped = false

  function open() {
    if (stopped) return
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    ws = new WebSocket(`${proto}//${location.host}${path}`)
    ws.onopen = () => {
      attempt = 0
    }
    ws.onmessage = (ev) => {
      try {
        onFrame(JSON.parse(ev.data as string) as Frame)
      } catch {
        // 坏帧丢掉即可：一条解不开的帧不该让整条通道断掉
      }
    }
    ws.onclose = () => {
      if (stopped) return
      const wait = backoff[Math.min(attempt, backoff.length - 1)]
      attempt++
      timer = setTimeout(open, wait)
    }
    ws.onerror = () => ws?.close()
  }
  open()

  return () => {
    stopped = true
    if (timer) clearTimeout(timer)
    ws?.close()
  }
}
