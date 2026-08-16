import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useEventsStore } from './events'

describe('事件流', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('事件帧头插，最新的在最前', () => {
    const s = useEventsStore()
    s.applyFrame({ type: 'event', data: { t: '10:00:00', node: 'a', kind: 'warn', msg: '第一条' } })
    s.applyFrame({ type: 'event', data: { t: '10:00:01', node: 'b', kind: 'crit', msg: '第二条' } })
    expect(s.events[0].msg).toBe('第二条')
    expect(s.events).toHaveLength(2)
  })

  // 环形缓冲：事件流会一直涨，不限长会把内存吃光，页面也会越来越卡。
  it('只保留最近 N 条', () => {
    const s = useEventsStore()
    for (let i = 0; i < 100; i++) {
      s.applyFrame({ type: 'event', data: { t: '10:00:00', node: 'a', kind: 'ok', msg: `第 ${i} 条` } })
    }
    expect(s.events.length).toBeLessThanOrEqual(40)
    // 保留的必须是**最新**的那些，不是最旧的
    expect(s.events[0].msg).toBe('第 99 条')
  })

  it('不认识的帧类型不影响事件流', () => {
    const s = useEventsStore()
    s.applyFrame({ type: 'heartbeat', data: { id: 'a', cpu: 1 } })
    expect(s.events).toHaveLength(0)
  })
})

describe('KPI 联动筛选', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('按状态筛选节点', async () => {
    const { useNodesStore } = await import('./nodes')
    const s = useNodesStore()
    s.__setFetcher(async () => ({
      baseline: 'cfg-1',
      drifted: ['n2'],
      nodes: [
        { id: 'n1', city: 'A', vendor: '', line: '', ip: '', status: 'ok', cfg: 'cfg-1', dns: true, drifted: false, last_hb: '', cpu: 1, mem: 1, conns: 1 },
        { id: 'n2', city: 'B', vendor: '', line: '', ip: '', status: 'down', cfg: 'cfg-0', dns: false, drifted: true, last_hb: '', cpu: 0, mem: 0, conns: 0 },
      ],
    }))
    await s.load()

    expect(s.filtered('all')).toHaveLength(2)
    expect(s.filtered('online').map((n) => n.id)).toEqual(['n1'])
    expect(s.filtered('down').map((n) => n.id)).toEqual(['n2'])
    expect(s.filtered('drifted').map((n) => n.id)).toEqual(['n2'])
  })
})
