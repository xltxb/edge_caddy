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
