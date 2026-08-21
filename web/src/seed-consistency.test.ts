import { describe, expect, it } from 'vitest'
import * as seed from '../mocks/seed'

/*
 * 夹具自己不能自相矛盾。
 *
 * 一个夹具表达不了的状态等于在开发期不存在——这条我们撞过好几次。但还有一种
 * 更难看的：**夹具表达了一个不可能的状态**。那时界面上的每一句都「对」，
 * 而它们对不上账，人会以为是界面在乱说。
 *
 * 真实的一例：node-de-01 有 drained_at（被下线），而 dns_reason 被推成了
 * manual —— DNS 页说「已暂停（abiu）」，节点页说「已下线（人为）」，两页各说
 * 各的。**我是看截图才发现的**，而截图不是每次都看。
 */
describe('节点夹具的内部一致性', () => {
  it('被下线的节点，dns_reason 必须是 drained', () => {
    for (const n of seed.nodes) {
      if (!n.drained_at) continue
      expect(n.dns_reason, `${n.id} 被下线了却记成 ${n.dns_reason}`).toBe('drained')
      expect(n.dns_enabled, `${n.id} 被下线了，解析不该还开着`).toBe(false)
    }
  })

  it('解析开着的节点，三个「谁关的」字段都该是空', () => {
    for (const n of seed.nodes.filter((x) => x.dns_enabled)) {
      expect(n.dns_reason, `${n.id} 解析开着却记着原因`).toBe('')
      expect(n.dns_actor, `${n.id} 解析开着却记着操作人`).toBeNull()
      expect(n.dns_changed_at, `${n.id} 解析开着却记着改动时刻`).toBeNull()
    }
  })

  /*
   * 系统自动摘除时不该有操作人 —— 后端给的是 null，夹具里编一个出来的话，
   * 界面上那条「不编造操作人」的测试就永远走不到真实数据。
   */
  it('auto_offline 不带操作人', () => {
    for (const n of seed.nodes.filter((x) => x.dns_reason === 'auto_offline')) {
      expect(n.dns_actor, `${n.id} 是系统摘的却记着操作人`).toBeNull()
    }
  })

  it('manual 要带操作人 —— 不然界面显示不出是谁关的', () => {
    for (const n of seed.nodes.filter((x) => x.dns_reason === 'manual')) {
      expect(n.dns_actor, `${n.id} 是人手动关的却没记操作人`).toBeTruthy()
    }
  })

  /*
   * 正面自检：上面几条全是「for 里过滤后断言」，**过滤空了它们一条都不会红**。
   * 所以先证明这几种状态在夹具里真的都存在 —— 否则这一组测的是「没有数据」。
   */
  it('这几种状态夹具里都有，否则上面几条测的是空集', () => {
    const reasons = new Set(seed.nodes.map((n) => n.dns_reason))
    expect(reasons, '夹具里缺了某一支').toEqual(new Set(['', 'drained', 'auto_offline']))
    expect(seed.nodes.some((n) => n.drained_at), '没有被下线的节点').toBe(true)
    expect(seed.nodes.some((n) => n.dns_enabled), '没有解析开着的节点').toBe(true)
  })
})
