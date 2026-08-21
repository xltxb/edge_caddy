import type { LinkState, WsFrame } from './types'

const WS_PATH = import.meta.env.VITE_WS_PATH ?? '/api/v1/ws'

/** 重连退避：140ms 起，翻倍，上限 8s。 */
const BACKOFF_START = 140
const BACKOFF_MAX = 8_000
/** 连续失败到这个次数就承认「实时挂了」，转入轮询降级并告诉用户。 */
const DEGRADE_AFTER = 3

type FrameHandler = (frame: WsFrame) => void
type StateHandler = (state: LinkState) => void

export interface EdgeSocket {
  start(): void
  stop(): void
  readonly state: LinkState
}

export interface EdgeSocketOptions {
  onFrame: FrameHandler
  onState: StateHandler
}

/**
 * 主控实时通道。
 *
 * 整个控制台的实时性都挂在这条连接上，所以它断了必须让用户看见——否则人对着
 * 一屏静止的旧数据以为一切正常。这跟 ADR-0002 提醒的「界面不能给出兑现不了的
 * 承诺」是同一类错。降级后的 2s 轮询由调用方在 onState 里接。
 */
export function createEdgeSocket({ onFrame, onState }: EdgeSocketOptions): EdgeSocket {
  let sock: WebSocket | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let backoff = BACKOFF_START
  let failures = 0
  let stopped = false
  let state: LinkState = 'connecting'

  const setState = (next: LinkState) => {
    if (state === next) return
    state = next
    onState(next)
  }

  const url = () => {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    return `${proto}//${location.host}${WS_PATH}`
  }

  const scheduleReconnect = () => {
    if (stopped) return
    failures += 1
    setState(failures >= DEGRADE_AFTER ? 'polling' : 'reconnecting')
    timer = setTimeout(connect, backoff)
    backoff = Math.min(backoff * 2, BACKOFF_MAX)
  }

  function connect(): void {
    if (stopped) return
    if (state !== 'polling' && state !== 'reconnecting') setState('connecting')

    let ws: WebSocket
    try {
      ws = new WebSocket(url())
    } catch {
      scheduleReconnect()
      return
    }
    sock = ws

    ws.onopen = () => {
      backoff = BACKOFF_START
      failures = 0
      setState('live')
    }

    ws.onmessage = (ev) => {
      let frame: WsFrame
      try {
        frame = JSON.parse(ev.data as string) as WsFrame
      } catch {
        return // 坏帧就丢掉，不该让一条脏数据打断整条连接
      }
      onFrame(frame)
    }

    ws.onerror = () => {
      // onerror 之后浏览器一定会再给一次 onclose，重连统一在那里做。
      ws.close()
    }

    ws.onclose = () => {
      if (sock === ws) sock = null
      scheduleReconnect()
    }
  }

  return {
    start() {
      stopped = false
      connect()
    },
    stop() {
      stopped = true
      if (timer) clearTimeout(timer)
      timer = null
      sock?.close()
      sock = null
    },
    get state() {
      return state
    },
  }
}
