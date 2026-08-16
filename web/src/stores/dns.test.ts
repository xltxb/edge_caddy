import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useDnsStore, type ScheduleResponse } from './dns'

const seed: ScheduleResponse = {
  domain: 'cdn.example.com',
  lines: ['默认', '电信', '联通', '移动', '台湾', '境外'],
  nodes: [
    { id: 'node-hk-01', city: '香港', ip: '1.1.1.1', line: 'CN2 GIA', status: 'ok' },
    { id: 'node-us-01', city: '洛杉矶', ip: '2.2.2.2', line: '国际 BGP', status: 'down' },
  ] as never,
  weights: [
    { domain: 'cdn.example.com', node: 'node-hk-01', line: '电信', weight: 60 },
    { domain: 'cdn.example.com', node: 'node-us-01', line: '电信', weight: 40 },
  ],
  planned: [{ NodeID: 'node-hk-01', IP: '1.1.1.1', Line: '电信', Weight: 100 }] as never,
  live: [{ ID: '1', Sub: '@', Value: '1.1.1.1', Line: '电信', Weight: 60 }] as never,
  drifted: true,
  drift_summary: '1.1.1.1（电信）权重库里 100、线上 60',
}

function io(over: Partial<ScheduleResponse> = {}) {
  return async () => ({ ...seed, ...over })
}

describe('DNS 调度 store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('载入后按线路分组', async () => {
    const s = useDnsStore()
    s.__setIO(io(), async () => ({ saved: true, applied: true }))
    await s.load('cdn.example.com')

    const telecom = s.rowsOf('电信')
    expect(telecom).toHaveLength(2)
    expect(telecom.find((r) => r.node === 'node-hk-01')!.weight).toBe(60)
  })

  // 改动**实时重算占比**，且只算在线的——那才是流量真正会怎么分。
  it('改权重实时重算占比，离线节点不计入', async () => {
    const s = useDnsStore()
    s.__setIO(io(), async () => ({ saved: true, applied: true }))
    await s.load('cdn.example.com')

    // node-us-01 是 down 的：它不参与解析，因此香港那台占 100%
    expect(s.shareOf('电信', 'node-hk-01')).toBe(100)

    s.setWeight('电信', 'node-hk-01', 30)
    // 仍然只有它在线，占比还是 100%
    expect(s.shareOf('电信', 'node-hk-01')).toBe(100)
    expect(s.shareOf('电信', 'node-us-01')).toBe(0)
  })

  it('权重为 0 的节点占比为 0', async () => {
    const s = useDnsStore()
    s.__setIO(io({
      nodes: [
        { id: 'node-a', city: 'A', ip: '1.1.1.1', line: '', status: 'ok' },
        { id: 'node-b', city: 'B', ip: '2.2.2.2', line: '', status: 'ok' },
      ] as never,
      weights: [
        { domain: 'd', node: 'node-a', line: '电信', weight: 100 },
        { domain: 'd', node: 'node-b', line: '电信', weight: 0 },
      ],
    }), async () => ({ saved: true, applied: true }))
    await s.load('d')

    expect(s.shareOf('电信', 'node-a')).toBe(100)
    expect(s.shareOf('电信', 'node-b')).toBe(0)
  })

  // 库里与线上不一致时必须能看出来。
  it('漂移可见，并给出后端的摘要', async () => {
    const s = useDnsStore()
    s.__setIO(io(), async () => ({ saved: true, applied: true }))
    await s.load('cdn.example.com')

    expect(s.drifted).toBe(true)
    expect(s.driftSummary).toContain('线上 60')
  })

  // 读不到线上时**说出来**，不显示成「已同步」。
  it('读不到线上解析时明确提示，不装作已同步', async () => {
    const s = useDnsStore()
    s.__setIO(io({ drifted: undefined, drift_summary: undefined, live_error: '凭据无效' } as never),
      async () => ({ saved: true, applied: true }))
    await s.load('cdn.example.com')

    expect(s.liveError).toContain('凭据无效')
    expect(s.drifted).toBe(false)
    // 关键：不能因为「没有漂移」就显示成已同步
    expect(s.syncState).toBe('unknown')
  })

  it('一致时状态是已同步', async () => {
    const s = useDnsStore()
    s.__setIO(io({ drifted: false, drift_summary: '' }), async () => ({ saved: true, applied: true }))
    await s.load('cdn.example.com')
    expect(s.syncState).toBe('synced')
  })

  // 保存失败时**不能**显示成成功。
  it('下发失败保留原因，不显示成功', async () => {
    const s = useDnsStore()
    s.__setIO(io(), async () => {
      throw new Error('权重已保存，但下发到 DNS 服务商失败：登录失败')
    })
    await s.load('cdn.example.com')
    await s.save(true)

    expect(s.saveError).toContain('登录失败')
    expect(s.lastApplied).toBe(false)
  })

  // 提交的是**相对权重**，不是算出来的百分比。
  it('提交相对权重而不是百分比', async () => {
    const s = useDnsStore()
    let sent: Record<string, unknown> = {}
    s.__setIO(io(), async (_d, body) => {
      sent = body as Record<string, unknown>
      return { saved: true, applied: true }
    })
    await s.load('cdn.example.com')
    s.setWeight('电信', 'node-hk-01', 7)
    await s.save(true)

    const ws = sent.weights as { node: string; weight: number }[]
    expect(ws.find((w) => w.node === 'node-hk-01')!.weight).toBe(7)
    expect(sent.apply).toBe(true)
  })
})
