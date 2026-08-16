import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useNodesStore } from './nodes'
import type { NodesResponse } from '@/api/types'

// 种子数据按真实响应的完整形状写。缺字段时放宽类型只会把「测试数据与真实
// 响应不一致」这件事藏起来，而那正是这类测试最容易失真的地方。
const seed: NodesResponse = {
  baseline: 'cfg-2f9a1c',
  nodes: [
    { id: 'node-hk-01', city: '香港', vendor: 'DMIT PPro', line: 'CN2 GIA', ip: '103.117.44.18',
      status: 'ok', cfg: 'cfg-2f9a1c', dns: true, drifted: false, last_hb: '2026-08-16T04:00:00Z',
      cpu: 15.2, mem: 32.8, conns: 0 },
    { id: 'node-us-01', city: '洛杉矶', vendor: 'Contabo', line: '国际 BGP', ip: '194.238.19.62',
      status: 'down', cfg: 'cfg-8b03e7', dns: false, drifted: true, last_hb: '2026-08-16T03:50:00Z',
      cpu: 0, mem: 0, conns: 0 },
  ],
  drifted: ['node-us-01'],
}

describe('节点 store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('载入后统计在线数与漂移数', async () => {
    const s = useNodesStore()
    s.__setFetcher(async () => seed)
    await s.load()

    expect(s.nodes).toHaveLength(2)
    expect(s.onlineCount).toBe(1)
    expect(s.driftedCount).toBe(1)
    expect(s.baseline).toBe('cfg-2f9a1c')
  })

  // 心跳帧必须**就地更新**已有的那一行，而不是往列表里追加。
  //
  // 追加的话，节点每跳一次心跳列表就长一条，3 秒一次，一分钟后列表里
  // 全是同一个节点。这是最容易写错、且一眼看不出来的地方——刚打开时
  // 完全正常。
  it('心跳帧就地更新对应节点，不追加新行', async () => {
    const s = useNodesStore()
    s.__setFetcher(async () => seed)
    await s.load()

    s.applyFrame({
      type: 'heartbeat',
      data: { id: 'node-hk-01', cpu: 88.5, mem: 41.2, hb_ms: 900, conns: 12400 },
    })

    expect(s.nodes).toHaveLength(2)
    const hk = s.nodes.find((n) => n.id === 'node-hk-01')!
    expect(hk.cpu).toBe(88.5)
    expect(hk.conns).toBe(12400)
    // 心跳只带负载，不该把别的字段冲掉
    expect(hk.city).toBe('香港')
    expect(hk.cfg).toBe('cfg-2f9a1c')
  })

  // 心跳来自一个列表里没有的节点时，忽略它。
  //
  // 凭一条心跳就凭空造一行，会让界面出现一个没有城市、没有厂商、没有 IP 的
  // 幽灵节点。节点的完整信息来自 /nodes，心跳只负责更新负载。
  it('忽略未知节点的心跳', async () => {
    const s = useNodesStore()
    s.__setFetcher(async () => seed)
    await s.load()

    s.applyFrame({ type: 'heartbeat', data: { id: 'node-never-seen', cpu: 1, mem: 2, hb_ms: 1, conns: 3 } })
    expect(s.nodes).toHaveLength(2)
    expect(s.nodes.some((n) => n.id === 'node-never-seen')).toBe(false)
  })

  it('不认识的帧类型不会破坏现有状态', async () => {
    const s = useNodesStore()
    s.__setFetcher(async () => seed)
    await s.load()

    s.applyFrame({ type: 'something_new', data: { whatever: true } })
    expect(s.nodes).toHaveLength(2)
    expect(s.onlineCount).toBe(1)
  })
})

describe('节点操作', () => {
  beforeEach(() => setActivePinia(createPinia()))

  async function ready() {
    const s = useNodesStore()
    s.__setFetcher(async () => seed)
    await s.load()
    return s
  }

  it('执行操作时置忙，完成后记录结果', async () => {
    const s = await ready()
    let release!: (v: unknown) => void
    s.__setPoster(() => new Promise((r) => (release = r)))

    const done = s.runOp('probe', 'node-hk-01')
    // 忙碌标记要精确到「哪个节点的哪个操作」——只有一个全局 boolean 的话，
    // 点了香港那台，整张表的按钮都会转圈。
    expect(s.busyOp).toBe('probe:node-hk-01')
    release({ ok: true, detail: '3ms' })
    await done

    expect(s.busyOp).toBe('')
    expect(s.opLog[0]).toMatchObject({ verb: 'probe', node: 'node-hk-01', ok: true })
  })

  // 失败必须记进 opLog 而不是静默吞掉：节点未连接会返回 404，
  // 这正是运维最需要看到的那一条。
  it('失败也记录，并且解除忙碌', async () => {
    const s = await ready()
    s.__setPoster(async () => {
      throw new Error('节点 node-us-01 当前未连接')
    })

    await s.runOp('push', 'node-us-01')
    expect(s.busyOp).toBe('')
    expect(s.opLog[0]).toMatchObject({ verb: 'push', node: 'node-us-01', ok: false })
    expect(s.opLog[0].detail).toContain('未连接')
  })

  // 同一节点的同一操作不重复并发：连点两下「重推」不该真推两次。
  it('忙碌期间对同一操作的重复调用被忽略', async () => {
    const s = await ready()
    let calls = 0
    let release!: (v: unknown) => void
    s.__setPoster(() => {
      calls++
      return new Promise((r) => (release = r))
    })

    const a = s.runOp('push', 'node-hk-01')
    const b = s.runOp('push', 'node-hk-01')
    release({ ok: true, detail: 'cfg-1' })
    await Promise.all([a, b])
    expect(calls).toBe(1)
  })
})

describe('节点行展开', () => {
  beforeEach(() => setActivePinia(createPinia()))

  async function ready() {
    const s = useNodesStore()
    s.__setFetcher(async () => seed)
    await s.load()
    return s
  }

  // 展开要显示的三样东西来自**同一次**探活回报。
  // 分三次取会各自看到不同的瞬间，拼出来的画面从未真实存在过。
  it('展开时探活一次，三样信息同源', async () => {
    const s = await ready()
    let calls = 0
    s.__setPoster(async () => {
      calls++
      return {
        node_id: 'node-hk-01', rtt_ms: 42, cfg_version: 'cfg-2f9a1c',
        caddy_ok: true, caddy_detail: 'Admin API 正常应答',
        logs: ['2026-08-16T00:00:00Z INFO 配置已生效 cfg_version=cfg-2f9a1c'],
      }
    })

    await s.expand('node-hk-01')
    expect(calls).toBe(1)
    const d = s.detail['node-hk-01']
    expect(d.rttMs).toBe(42)
    expect(d.cfgVersion).toBe('cfg-2f9a1c')
    expect(d.caddyOk).toBe(true)
    expect(d.logs).toHaveLength(1)
  })

  // Caddy 挂了不是探活失败：展开照样能看到日志——那正是要查的东西。
  it('Caddy 不可达时仍展示日志与原因', async () => {
    const s = await ready()
    s.__setPoster(async () => ({
      rtt_ms: 5, cfg_version: '', caddy_ok: false,
      caddy_detail: 'connection refused', logs: ['... ERROR 应用配置失败'],
    }))

    await s.expand('node-hk-01')
    const d = s.detail['node-hk-01']
    expect(d.caddyOk).toBe(false)
    expect(d.caddyDetail).toBe('connection refused')
    expect(d.logs).toHaveLength(1)
  })

  // 探活失败时要留下错误，而不是留一个空壳让界面显示「Caddy 不可达」——
  // 那会把「没问上」说成「问到了，答案是坏的」。
  it('探活失败时记录错误而不是伪造一份空状态', async () => {
    const s = await ready()
    s.__setPoster(async () => {
      throw new Error('节点 node-hk-01 当前未连接')
    })

    await s.expand('node-hk-01')
    const d = s.detail['node-hk-01']
    expect(d.error).toContain('未连接')
    expect(d.caddyOk).toBeUndefined()
  })
})
