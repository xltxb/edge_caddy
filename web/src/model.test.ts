import { describe, expect, it } from 'vitest'
import { fromEventWire, fromKpiWire, fromNodeWire, hbAgeSec, orDash } from './model'
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
  drained_at: null,
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
      nodes_online: 3,
      nodes_warn: 2,
      nodes_down: 1,
      nodes_total: 6,
      conns_total: 48200,
      conns_delta_pct: null,
      origin_rate: 8.7,
      drift_nodes: 1,
    }
    expect(fromKpiWire(w).connsDeltaPct).toBeNull()
  })

  it('三档相加等于总数 —— 这个不变量由后端一条语句保证，前端不再推导', () => {
    const w: OverviewKpiWire = {
      nodes_online: 3,
      nodes_warn: 2,
      nodes_down: 1,
      nodes_total: 6,
      conns_total: 0,
      conns_delta_pct: null,
      origin_rate: null,
      drift_nodes: 0,
    }
    const k = fromKpiWire(w)
    expect(k.nodesOnline + k.nodesWarn + k.nodesDown).toBe(k.nodesTotal)
  })

  it('origin_rate 为 null 时原样保留 —— 不能当成 0', () => {
    // 「0% 回源」意味着边缘挡下了全部请求，那是一个很强的说法，
    // 而真相是我们还不知道。
    const w: OverviewKpiWire = {
      nodes_online: 1,
      nodes_warn: 0,
      nodes_down: 0,
      nodes_total: 1,
      conns_total: 0,
      conns_delta_pct: null,
      origin_rate: null,
      drift_nodes: 0,
    }
    expect(fromKpiWire(w).originRate).toBeNull()
  })
})

describe('orDash', () => {
  it('null 与 undefined 都当作没有值', () => {
    expect(orDash(null)).toBe('—')
    expect(orDash(undefined)).toBe('—')
  })

  it('空串也当作没有值 —— 契约说该用 null，但线上会收到空串', () => {
    // 只判 null 的话，空串会原样渲染成一个空位，看起来像界面漏了内容
    expect(orDash('')).toBe('—')
  })

  it('有值时原样返回', () => {
    expect(orDash('node-hk-01')).toBe('node-hk-01')
  })
})

describe('fromEventWire 的空节点', () => {
  it('空串与 null 都收成破折号', () => {
    const base = { id: 1, at: '', kind: 'ok' as const, msg: 'x' }
    expect(fromEventWire({ ...base, node: null }).node).toBe('—')
    expect(fromEventWire({ ...base, node: '' }).node).toBe('—')
  })
})
