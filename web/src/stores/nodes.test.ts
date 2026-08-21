import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useNodesStore } from './nodes'
import { fromNodeWire } from '@/model'
import type { HeartbeatFrame, NodeWire } from '@/api/types'

const wire = (id: string, cfg: string): NodeWire => ({
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
  routes: 4,
  rules: 3,
  created_at: '',
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
