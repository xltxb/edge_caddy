import { describe, expect, it } from 'vitest'
import { fromEventWire, fromKpiWire, fromNodeWire, hbAgeSec } from './model'
import type { EventWire, NodeWire, OverviewKpiWire } from './api/types'

const wire = (over: Partial<NodeWire> = {}): NodeWire => ({
  id: 'node-hk-01',
  city: '香港',
  vendor: 'DMIT PPro',
  line: 'CN2 GIA',
  public_ip: '203.0.113.7',
  status: 'ok',
  cpu: 15.2,
  mem: 32.8,
  conns: 12400,
  cpu_series: [1, 2, 3],
  last_hb_at: '2026-08-21T10:42:05+08:00',
  hb_age_ms: 1200,
  cfg_version: 'cfg-2f9a1c',
  drift: false,
  dns_enabled: true,
  routes: 4,
  rules: 3,
  created_at: '2026-08-01T09:00:00+08:00',
  ...over,
})

describe('fromNodeWire', () => {
  it('cpu_series 为 null 时收成空数组，界面留白而不是崩', () => {
    // 主控重启后这个字段会空几十秒（契约 §4），是正常状态不是错误
    expect(fromNodeWire(wire({ cpu_series: null })).cpuSeries).toEqual([])
  })

  it('记下收到心跳的本地时刻，供之后本地计时', () => {
    const n = fromNodeWire(wire({ hb_age_ms: 1200 }), 1_000_000)
    expect(n.hbAgeMs).toBe(1200)
    expect(n.hbStampedAt).toBe(1_000_000)
  })
})

describe('hbAgeSec', () => {
  it('年龄 = 服务端给的 hb_age_ms + 本地经过的时间', () => {
    // 不用浏览器时钟减 last_hb_at —— 那会被主控与浏览器的时钟偏差污染
    const n = fromNodeWire(wire({ hb_age_ms: 1200 }), 1_000_000)
    expect(hbAgeSec(n, 1_000_000)).toBeCloseTo(1.2)
    expect(hbAgeSec(n, 1_003_000)).toBeCloseTo(4.2)
  })

  it('本地时钟回拨时不给出负年龄', () => {
    const n = fromNodeWire(wire({ hb_age_ms: 1200 }), 1_000_000)
    expect(hbAgeSec(n, 999_000)).toBeCloseTo(1.2)
  })
})

describe('fromEventWire', () => {
  it('系统级事件的 node 为 null，收成破折号让界面不必判空', () => {
    const w: EventWire = { id: 1, at: '', node: null, kind: 'ok', msg: '下发完成' }
    expect(fromEventWire(w).node).toBe('—')
  })
})

describe('fromKpiWire', () => {
  it('conns_delta_pct 的 null 原样保留 —— 不能被当成 0', () => {
    // 0% 会被读成「持平」，而 null 的含义是「历史不足，还算不出来」
    const w: OverviewKpiWire = {
      nodes_online: 5,
      nodes_total: 6,
      conns_total: 48200,
      conns_delta_pct: null,
      origin_rate: 8.7,
      drift_nodes: 1,
    }
    expect(fromKpiWire(w).connsDeltaPct).toBeNull()
  })
})
