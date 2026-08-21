import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useNodesStore } from './nodes'
import { fromNodeWire, isZeroTime } from '@/model'
import type { HeartbeatFrame, NodeWire } from '@/api/types'

const getMock = vi.fn()
vi.mock('@/api/http', () => ({
  http: {
    get: (...a: unknown[]) => getMock(...a),
    post: vi.fn(),
    put: vi.fn(),
    del: vi.fn(),
  },
}))

const wire = (id: string, cfg: string, over: Partial<NodeWire> = {}): NodeWire => ({
  id,
  city: '香港',
  vendor: 'DMIT PPro',
  line: 'CN2 GIA',
  public_ip: '203.0.113.7',
  status: 'ok',
  cpu: 10,
  mem: 20,
  conns: 100,
  cpu_series: [1, 2, 3],
  last_hb_at: '',
  hb_age_ms: 0,
  cfg_version: cfg,
  drift: false,
  dns_enabled: true,
  drained_at: null,
  routes: 4,
  rules: 3,
  created_at: '',
  ...over,
})

const hb = (over: Partial<HeartbeatFrame['data']>): HeartbeatFrame => ({
  type: 'heartbeat',
  data: {
    id: 'node-a',
    status: 'ok',
    cpu: 50,
    mem: 40,
    conns: 999,
    hb_age_ms: 30,
    cfg_version: 'cfg-new',
    routes: 4,
    rules: 3,
    ...over,
  },
})

describe('useNodesStore', () => {
  beforeEach(() => setActivePinia(createPinia()))

  describe('recomputeDrift', () => {
    it('漂移只由「上报版本号 ≠ 基线」决定', () => {
      // ADR-0002：这个判断不看节点上的实际配置内容，只看版本号
      const store = useNodesStore()
      store.items = [wire('node-a', 'cfg-new'), wire('node-b', 'cfg-old')].map((w) =>
        fromNodeWire(w),
      )

      store.recomputeDrift('cfg-new')

      expect(store.items.find((n) => n.id === 'node-a')!.drift).toBe(false)
      expect(store.items.find((n) => n.id === 'node-b')!.drift).toBe(true)
      expect(store.drifted.map((n) => n.id)).toEqual(['node-b'])
    })

    it('基线变了之后原本一致的节点会变成漂移', () => {
      const store = useNodesStore()
      store.items = [fromNodeWire(wire('node-a', 'cfg-old'))]

      store.recomputeDrift('cfg-old')
      expect(store.items[0]!.drift).toBe(false)

      store.recomputeDrift('cfg-newer')
      expect(store.items[0]!.drift).toBe(true)
    })
  })

  describe('applyHeartbeat', () => {
    it('就地更新指标并把 CPU 追加进 sparkline', () => {
      const store = useNodesStore()
      store.items = [fromNodeWire(wire('node-a', 'cfg-old'))]

      store.applyHeartbeat(hb({ cpu: 77 }))

      const n = store.items[0]!
      expect(n.cpu).toBe(77)
      expect(n.conns).toBe(999)
      expect(n.cfgVersion).toBe('cfg-new')
      expect(n.cpuSeries.at(-1)).toBe(77)
    })

    it('sparkline 固定 12 点，超出从头部挤掉最旧的', () => {
      const store = useNodesStore()
      const w = wire('node-a', 'cfg-old')
      w.cpu_series = Array.from({ length: 12 }, (_, i) => i)
      store.items = [fromNodeWire(w)]

      store.applyHeartbeat(hb({ cpu: 99 }))

      const s = store.items[0]!.cpuSeries
      expect(s).toHaveLength(12)
      expect(s[0]).toBe(1) // 最旧的 0 被挤掉
      expect(s.at(-1)).toBe(99)
    })

    it('心跳提到不认识的节点时忽略 —— 成员由 REST 决定', () => {
      const store = useNodesStore()
      store.items = [fromNodeWire(wire('node-a', 'cfg-old'))]

      expect(() => store.applyHeartbeat(hb({ id: 'node-ghost' }))).not.toThrow()
      expect(store.items).toHaveLength(1)
      expect(store.items[0]!.cpu).toBe(10)
    })
  })
})

/*
 * 解析开关的两个事实必须分开。
 *
 * `dns_enabled` 是本地标志位，决定归一化里谁参与；解析记录真的变没变是另一件事。
 * 没同步时，一个「已退出解析」的徽标是常驻的谎 —— toast 会消失，徽标不会。
 */
describe('dns_sync：服务商那边真的这样了没有', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('与节点列表同一个响应取到，不额外发请求', async () => {
    getMock.mockReset().mockResolvedValue({
      items: [wire('node-hk-01', 'cfg-1')],
      baseline: 'cfg-1',
      dns_sync: { ok: false, at: '0001-01-01T00:00:00Z', detail: '尚未向 DNS 服务商同步过' },
    })
    const store = useNodesStore()
    await store.fetchAll()
    expect(getMock).toHaveBeenCalledTimes(1)
    expect(store.dnsSync?.ok).toBe(false)
    expect(store.dnsSync?.detail).toContain('尚未')
  })

  it('同步过就是 ok', async () => {
    getMock.mockReset().mockResolvedValue({
      items: [],
      baseline: 'cfg-1',
      dns_sync: { ok: true, at: '2026-08-21T15:40:00+08:00', detail: '已同步' },
    })
    const store = useNodesStore()
    await store.fetchAll()
    expect(store.dnsSync?.ok).toBe(true)
  })

  /*
   * 后端在「从没同步过」时给的是 Go 的零值时间，契约没规定这一点。
   * 不挡的话它会被格式化成一个像模像样的 00:00:00 —— 一个**格式正确但意思是
   * 假的**值，比一个空白危险得多。
   */
  it('零值时间要被认出来，不能当成一个真的时刻', () => {
    expect(isZeroTime('0001-01-01T00:00:00Z')).toBe(true)
    expect(isZeroTime('')).toBe(true)
    expect(isZeroTime(null)).toBe(true)
    expect(isZeroTime('2026-08-21T15:40:00+08:00')).toBe(false)
  })
})

/*
 * 下线是**意图**，离线是**观察**（CONTEXT.md、ADR-0014）。
 *
 * 一台节点可以「已下线且在线」（刚按下，隧道还没断干净），也可以「未下线但离线」
 * （它自己挂了）。前者是「我关的」，后者是故障 —— 把两者塞进同一个字段，
 * 心跳一来就会把人的意图冲掉。
 */
describe('下线与离线各记各的', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('drained_at 与 status 互不覆盖：可以「已下线且在线」', () => {
    const n = fromNodeWire(wire('node-hk-01', 'cfg-1', { status: 'ok', drained_at: '2026-08-21T16:46:50+08:00' }))
    expect(n.status).toBe('ok')
    expect(n.drainedAt).not.toBeNull()
  })

  it('也可以「未下线但离线」', () => {
    const n = fromNodeWire(wire('node-hk-01', 'cfg-1', { status: 'down', drained_at: null }))
    expect(n.status).toBe('down')
    expect(n.drainedAt).toBeNull()
  })

  // 心跳只带 status，不带意图 —— 一个下线过的节点又开始报心跳时，
  // 它仍然是「已下线」的，只是「在线」了。
  it('心跳不冲掉下线标记', () => {
    const store = useNodesStore()
    store.items = [fromNodeWire(wire('node-hk-01', 'cfg-1', { status: 'down', drained_at: '2026-08-21T16:46:50+08:00' }))]
    store.applyHeartbeat({
      type: 'heartbeat',
      data: {
        id: 'node-hk-01',
        status: 'ok',
        cpu: 5,
        mem: 5,
        conns: 1,
        hb_age_ms: 10,
        cfg_version: 'cfg-1',
        routes: 1,
        rules: 1,
      },
    })
    expect(store.items[0]!.status).toBe('ok')
    expect(store.items[0]!.drainedAt).not.toBeNull()
  })

  it('重新上线不顺手打开解析 —— 能接入不等于该马上分流量', async () => {
    getMock.mockReset().mockRejectedValue(new Error('不测刷新'))
    const postMock = vi.fn().mockResolvedValue({
      id: 'node-hk-01',
      drained_at: null,
      dns_enabled: false,
      detail: '已允许重新接入；解析仍是关闭的，确认配置无误后再打开',
    })
    const { http } = await import('@/api/http')
    ;(http as unknown as { post: unknown }).post = postMock

    const store = useNodesStore()
    const r = await store.rejoin('node-hk-01')
    expect(postMock).toHaveBeenCalledWith('/nodes/node-hk-01/rejoin')
    expect(r.drained_at).toBeNull()
    expect(r.dns_enabled).toBe(false)
  })
})
