import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useNodesStore } from './nodes'
import { fromNodeWire, isZeroTime } from '@/model'
import type { HeartbeatFrame, NodeWire } from '@/api/types'

const getMock = vi.fn()
const putMock = vi.fn()
const delMock = vi.fn()
vi.mock('@/api/http', () => ({
  http: {
    get: (...a: unknown[]) => getMock(...a),
    post: vi.fn(),
    put: (...a: unknown[]) => putMock(...a),
    del: (...a: unknown[]) => delMock(...a),
  },
  // store 确实 import 了它（fetchAll 的 catch 用），mock 里缺了就只有走到那条
  // 路径才炸 —— 而那正是「失败路径没人测」的那种炸法
  errorText: (e: unknown, fallback = '操作失败') =>
    e instanceof Error && e.message ? e.message : fallback,
}))

const wire = (id: string, cfg: string, over: Partial<NodeWire> = {}): NodeWire => ({
  id,
  city: '香港',
  vendor: 'DMIT PPro',
  line: 'CN2 GIA',
  public_ip: '203.0.113.7',
  status: 'ok',
  online: true,
  cpu: 10,
  mem: 20,
  conns: 100,
  cpu_series: [1, 2, 3],
  last_hb_at: '',
  hb_age_ms: 0,
  cfg_version: cfg,
  agent_version: 'v0.2.0 (d3da612)',
  drift: false,
  dns_enabled: true,
  drained_at: null,
  dns_reason: '' as const,
  dns_actor: null,
  dns_changed_at: null,
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
   * 这条保的是**形状**，不是「这段代码有用」。
   *
   * 零值时间在当前的后端行为下**走不到** —— 契约 §0.4 现在规定「从没同步过」
   * 给 `null`，而后端也照做了。留着 `isZeroTime` 是因为它挡的是一类值：
   * 一个**格式正确而意思是假的**时间戳，会被渲染成像模像样的 `00:00:00`，
   * 读起来像「凌晨同步过一次」；空白会让人去查，一个像样的时间不会。
   * 零值时间哪天从别的路径回来（新字段、另一个服务、序列化库换了），它还在。
   *
   * **写清这一点是因为一条一直绿的测试会替它覆盖的东西作保。** 后端那边有个
   * 更狠的例子：`traffic_samples` 那张表从没被任何 SELECT 读过，而每次迁移
   * 测试都断言它存在 —— 检查它存在，恰恰强化了「它是有用的」这个印象。
   * 一条测试保的到底是什么，得写在测试里，不能靠读的人推断。
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

  describe('改元数据', () => {
    beforeEach(() => putMock.mockReset())

    /*
     * **发出去的 body 里不能有 node_id。**
     *
     * 类型上挡着（`NodeUpdateBody` 没有那个键），但类型只管调用点写死的对象；
     * 真正发出去的是 store 转手的那一份，而 `{ ...form }` 这种写法会把表单上
     * 多出来的任何键一起带走。后端严格绑定，带了整个请求被 1001 拒 ——
     * 而那件事今天只在运行时才知道。
     */
    it('body 只有四项，不含 node_id', async () => {
      putMock.mockResolvedValue({
        id: 'node-a',
        city: '新加坡',
        vendor: 'V.PS',
        line: 'CMIN2',
        public_ip: '203.0.113.9',
        dns_synced: true,
        detail: '公网 IP 已改（203.0.113.7 → 203.0.113.9），解析已同步到服务商',
      })
      const store = useNodesStore()
      store.items = [fromNodeWire(wire('node-a', 'cfg-1'))]

      await store.updateNode('node-a', {
        city: '新加坡',
        vendor: 'V.PS',
        line: 'CMIN2',
        public_ip: '203.0.113.9',
      })

      const [path, body] = putMock.mock.calls[0]!
      expect(path).toBe('/nodes/node-a')
      expect(Object.keys(body as object).sort()).toEqual([
        'city',
        'line',
        'public_ip',
        'vendor',
      ])
    })

    it('就地更新那四项，不重拉全表', async () => {
      putMock.mockResolvedValue({
        id: 'node-a',
        city: '新加坡',
        vendor: 'V.PS',
        line: 'CMIN2',
        public_ip: '203.0.113.9',
        dns_synced: true,
        detail: '解析已同步',
      })
      getMock.mockReset()
      const store = useNodesStore()
      store.items = [fromNodeWire(wire('node-a', 'cfg-1'))]

      await store.updateNode('node-a', {
        city: '新加坡',
        vendor: 'V.PS',
        line: 'CMIN2',
        public_ip: '203.0.113.9',
      })

      const n = store.items[0]!
      expect([n.city, n.vendor, n.line, n.ip]).toEqual([
        '新加坡',
        'V.PS',
        'CMIN2',
        '203.0.113.9',
      ])
      // 重拉会把整页的心跳年龄、CPU 序列一起换掉 —— 改个城市名不该让满屏数字跳一下
      expect(getMock).not.toHaveBeenCalled()
    })

    /*
     * **dns_synced 原样交回调用方，store 不替它判成败。**
     *
     * `false` 有两种完全不同的意思：「这次改动跟解析无关」（detail 空串）和
     * 「解析该变而没变成」（detail 有话）。store 判不了 —— 那取决于人到底改没改
     * IP，而 store 手上只有一个布尔和一句话。在这里替它判，就是在最没有信息的
     * 地方做那个决定。
     */
    it('dns_synced 与 detail 原样返回，不在 store 里判成败', async () => {
      putMock.mockResolvedValue({
        id: 'node-a',
        city: '香港',
        vendor: 'DMIT PPro',
        line: 'CN2 GIA',
        public_ip: '203.0.113.7',
        dns_synced: false,
        detail: '',
      })
      const store = useNodesStore()
      store.items = [fromNodeWire(wire('node-a', 'cfg-1'))]

      const r = await store.updateNode('node-a', {
        city: '香港',
        vendor: 'DMIT PPro',
        line: 'CN2 GIA',
        public_ip: '203.0.113.7',
      })

      expect(r.dns_synced).toBe(false)
      expect(r.detail).toBe('')
    })
  })

  describe('删记录', () => {
    beforeEach(() => delMock.mockReset())

    it('删完就地移除那一行，不等下一轮请求', async () => {
      delMock.mockResolvedValue({ id: 'node-a', detail: '已删除记录。注意那台机器上的…' })
      getMock.mockReset()
      const store = useNodesStore()
      store.items = [
        fromNodeWire(wire('node-a', 'cfg-1')),
        fromNodeWire(wire('node-b', 'cfg-1')),
      ]

      await store.removeNode('node-a')

      expect(store.items.map((n) => n.id)).toEqual(['node-b'])
      expect(delMock).toHaveBeenCalledWith('/nodes/node-a')
      expect(getMock).not.toHaveBeenCalled()
    })

    /*
     * detail 要原样交回去 —— 它说的是这个操作**只做了一半**：记录没了，而那台
     * 机器上的 Agent 与 Caddy 还在跑。store 把它吞掉的话，界面就没有第二个
     * 地方能知道这件事。
     */
    it('detail 原样返回 —— 那是「另一半没做」的唯一出处', async () => {
      const detail =
        '已删除记录。注意那台机器上的 Agent 与 Caddy 还在跑，要真正撤掉在那台机器上执行：sudo ./edge-node.sh uninstall'
      delMock.mockResolvedValue({ id: 'node-a', detail })
      const store = useNodesStore()
      store.items = [fromNodeWire(wire('node-a', 'cfg-1'))]

      const r = await store.removeNode('node-a')

      expect(r.detail).toBe(detail)
    })

    it('删失败时那一行还在 —— 不能先删界面再等结果', async () => {
      // mockRejectedValue 会**立刻**建出那个 rejected promise，在 await 到它
      // 之前就先被判成 unhandled rejection。调用时才建，就没有那个窗口。
      delMock.mockImplementationOnce(() => Promise.reject(new Error('该节点还连着，先下线再删')))
      const store = useNodesStore()
      store.items = [fromNodeWire(wire('node-a', 'cfg-1'))]

      let caught: unknown = null
      try {
        await store.removeNode('node-a')
      } catch (e) {
        caught = e
      }

      expect((caught as Error).message).toContain('先下线')
      expect(store.items.map((n) => n.id)).toEqual(['node-a'])
    })
  })
})
