import { describe, expect, it } from 'vitest'
import { canEnableDns, nodeFlags } from './flags'
import type { EdgeNode } from '@/model'

const node = (over: Partial<EdgeNode> = {}): EdgeNode => ({
  id: 'node-hk-01',
  city: '香港',
  vendor: 'DMIT PPro',
  line: 'CN2 GIA',
  ip: '203.0.113.7',
  status: 'ok',
  cpu: 10,
  mem: 20,
  conns: 100,
  cpuSeries: [],
  hbAgeMs: 0,
  hbStampedAt: 0,
  cfgVersion: 'cfg-1',
  drift: false,
  dnsEnabled: true,
  drainedAt: null,
  routes: 1,
  rules: 1,
  ...over,
})

const texts = (n: EdgeNode, sync: boolean | null = true) => nodeFlags(n, sync).map((f) => f.text)

describe('下线与离线不合并', () => {
  /*
   * 这一组对应 ADR-0014 的**前提**，不是它的结论。
   *
   * 结论是「分两列存」，那个后端有测试。前提是「这两件事会同时成立，而且各要
   * 各的说法」—— 前提不成立的话，分两列也是白分：界面照样可以把它们揉回一格。
   */
  it('已下线且在线：旗标只说人为下线，status 那一格另说', () => {
    const n = node({ status: 'ok', drainedAt: '2026-08-21T16:52:26+08:00' })
    expect(texts(n)).toContain('已下线（人为）')
    // status 不进旗标 —— 它由 VStatusPill 单独占一格
    expect(texts(n).join()).not.toContain('离线')
  })

  it('未下线但离线：不冒出「已下线」', () => {
    expect(texts(node({ status: 'down' }))).not.toContain('已下线（人为）')
  })

  it('两者可以同时成立', () => {
    const n = node({ status: 'down', drainedAt: '2026-08-21T16:52:26+08:00' })
    expect(texts(n)).toContain('已下线（人为）')
    expect(n.status).toBe('down')
  })
})

describe('「已退出解析」是关于服务商的断言', () => {
  /*
   * 前提：dns_enabled 只是本地标志位，解析记录变没变是另一件事。
   * 没同步时说「已退出解析」是**常驻的谎** —— toast 会消失，旗标不会。
   */
  it('同步过：说「已退出解析」', () => {
    expect(texts(node({ dnsEnabled: false }), true)).toContain('已退出解析')
  })

  it('没同步：降级成「已标记退出（解析未变）」', () => {
    expect(texts(node({ dnsEnabled: false }), false)).toContain('已标记退出（解析未变）')
  })

  // 没问到就不加限定：宁可少说一句，也不要因为自己没问到就说节点在撒谎
  it('还没问到（null）：按同步过说，不擅自降级', () => {
    expect(texts(node({ dnsEnabled: false }), null)).toContain('已退出解析')
  })

  it('解析开着时根本不出这条旗标', () => {
    expect(texts(node({ dnsEnabled: true }), false)).toHaveLength(0)
  })
})

describe('「恢复解析」在已下线时不该能按', () => {
  it('已下线且解析关着：拦住，并说清怎么办', () => {
    const r = canEnableDns(node({ dnsEnabled: false, drainedAt: '2026-08-21T16:52:26+08:00' }))
    expect(r.ok).toBe(false)
    expect(r.reason).toContain('重新上线')
  })

  // 关解析后端不拒，所以只在「要开」的方向拦 —— 拦错方向会让人没法暂停解析
  it('已下线但解析开着：那是「关」的方向，不拦', () => {
    expect(canEnableDns(node({ dnsEnabled: true, drainedAt: '2026-08-21T16:52:26+08:00' })).ok).toBe(
      true,
    )
  })

  it('没下线：正常可按', () => {
    expect(canEnableDns(node({ dnsEnabled: false })).ok).toBe(true)
  })
})
