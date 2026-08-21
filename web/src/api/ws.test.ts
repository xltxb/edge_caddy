import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createEdgeSocket } from './ws'
import type { LinkState, WsFrame } from './types'

/**
 * 假 WebSocket —— 只实现 ws.ts 真正用到的那几个口子。
 * 测的是「断线之后前端怎么表现」，这条路径在真实环境里很难稳定复现，
 * 但它恰恰是用户最需要被告知的时刻，所以必须有测试守着。
 */
class FakeSocket {
  static readonly OPEN = 1
  static instances: FakeSocket[] = []

  readonly OPEN = 1
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null
  closed = false

  constructor(readonly url: string) {
    FakeSocket.instances.push(this)
  }

  open(): void {
    this.readyState = 1
    this.onopen?.()
  }

  drop(): void {
    this.readyState = 3
    this.onclose?.()
  }

  deliver(payload: unknown): void {
    this.onmessage?.({ data: typeof payload === 'string' ? payload : JSON.stringify(payload) })
  }

  close(): void {
    this.closed = true
  }
}

const latest = () => FakeSocket.instances[FakeSocket.instances.length - 1]!

describe('createEdgeSocket', () => {
  let states: LinkState[]
  let frames: WsFrame[]

  beforeEach(() => {
    vi.useFakeTimers()
    FakeSocket.instances = []
    states = []
    frames = []
    vi.stubGlobal('WebSocket', FakeSocket)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  const make = () =>
    createEdgeSocket({
      onFrame: (f) => frames.push(f),
      onState: (s) => states.push(s),
    })

  it('建连成功后进入 live', () => {
    const sock = make()
    sock.start()
    latest().open()

    expect(sock.state).toBe('live')
    expect(states).toContain('live')
    sock.stop()
  })

  it('断开后先重连，连续失败到阈值才承认降级', () => {
    const sock = make()
    sock.start()
    latest().open()
    expect(sock.state).toBe('live')

    // 第 1 次断开：还在重连，不该马上吓唬用户
    latest().drop()
    expect(sock.state).toBe('reconnecting')

    // 退避 140ms 后重试，仍然失败
    vi.advanceTimersByTime(140)
    latest().drop()
    expect(sock.state).toBe('reconnecting')

    // 第 3 次失败 —— 到这里必须承认「已经不是实时的了」
    vi.advanceTimersByTime(280)
    latest().drop()
    expect(sock.state).toBe('polling')

    sock.stop()
  })

  it('重连成功后回到 live 并把退避重置', () => {
    const sock = make()
    sock.start()
    latest().open()
    latest().drop()
    vi.advanceTimersByTime(140)
    latest().open()

    expect(sock.state).toBe('live')

    // 退避已重置：再断一次，仍然是最短的 140ms 就重试
    latest().drop()
    const before = FakeSocket.instances.length
    vi.advanceTimersByTime(140)
    expect(FakeSocket.instances.length).toBe(before + 1)

    sock.stop()
  })

  it('坏帧被丢掉，不影响连接也不影响后续好帧', () => {
    const sock = make()
    sock.start()
    latest().open()

    latest().deliver('{ 这不是 JSON')
    expect(frames).toHaveLength(0)
    expect(sock.state).toBe('live')

    latest().deliver({ type: 'event', data: { id: 1, at: '', node: null, kind: 'ok', msg: 'x' } })
    expect(frames).toHaveLength(1)

    sock.stop()
  })

  it('stop 之后不再重连', () => {
    const sock = make()
    sock.start()
    latest().open()
    sock.stop()

    const before = FakeSocket.instances.length
    vi.advanceTimersByTime(10_000)
    expect(FakeSocket.instances.length).toBe(before)
  })
})
