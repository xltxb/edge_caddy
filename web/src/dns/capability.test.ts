import { describe, expect, it } from 'vitest'
import {
  computeShares,
  isDivergent,
  isIdle,
  lineInputs,
  mergedWeight,
  type LineInput,
} from './capability'

const CONTRACT = [
  { code: 'ct', name: '电信' },
  { code: 'cu', name: '联通' },
  { code: 'cm', name: '移动' },
  { code: 'tw', name: '台湾' },
  { code: 'ov', name: '境外 / 默认' },
]

/** 后端给的能力线路。covers 由服务商决定，前端不再持有这张表。 */
const CN = { code: 'cn', name: '中国（电信 / 联通 / 移动合并）', covers: ['ct', 'cu', 'cm'] }
const TW = { code: 'tw', name: '台湾', covers: ['tw'] }
const OV = { code: 'ov', name: '境外', covers: ['ov'] }
const EACH = CONTRACT.map((l) => ({ code: l.code, name: l.name, covers: [l.code] }))

describe('lineInputs', () => {
  it('没有服务商时按契约的五条线原样渲染', () => {
    // 那时权重只是本地意图，没有服务商会拒绝它
    const g = lineInputs(CONTRACT, undefined)
    expect(g).toHaveLength(5)
    expect(g.every((x) => x.supported && x.covers.length === 1)).toBe(true)
  })

  it('Cloudflare：电信/联通/移动合并成一个「中国」', () => {
    // 它的 DNS 记录没有线路概念，三者表达不了 —— 合并让非法状态无法被表达，
    // 比让人配完三个不同的数字再拒绝要好
    const g = lineInputs(CONTRACT, [CN, TW, OV])
    expect(g.map((x) => x.code)).toEqual(['cn', 'tw', 'ov'])
    expect(g[0]!.covers).toEqual(['ct', 'cu', 'cm'])
    expect(g[0]!.name).toContain('合并')
  })

  it('DNSPod：五条线分别可配', () => {
    const g = lineInputs(CONTRACT, EACH)
    expect(g).toHaveLength(5)
    expect(g.every((x) => x.covers.length === 1 && x.supported)).toBe(true)
  })

  it('服务商覆盖不到的线路仍然列出，但禁用', () => {
    // 悄悄藏掉会让人以为这条线路不存在，而它在契约里是存在的
    const g = lineInputs(CONTRACT, [CN])
    const tw = g.find((x) => x.code === 'tw')!
    expect(tw.supported).toBe(false)
    expect(g.filter((x) => !x.supported).map((x) => x.code)).toEqual(['tw', 'ov'])
  })

  it('服务商报了契约里没有的线路码时忽略它', () => {
    // 覆盖关系由服务商给，但我们只画得出契约里存在的线路
    const g = lineInputs(CONTRACT, [CN, { code: 'zz', name: '火星', covers: ['zz'] }])
    expect(g.some((x) => x.code === 'zz')).toBe(false)
  })

  it('不认识的能力码照样能渲染 —— covers 由后端给，前端不需要认识它', () => {
    // 加第三家服务商时前端不用改：它可能报 apac 覆盖台湾与境外
    const g = lineInputs(CONTRACT, [
      { code: 'apac', name: '亚太', covers: ['tw', 'ov'] },
      CN,
    ])
    const apac = g.find((x) => x.code === 'apac')!
    expect(apac.covers).toEqual(['tw', 'ov'])
    expect(apac.supported).toBe(true)
    expect(g.filter((x) => !x.supported)).toHaveLength(0)
  })
})

describe('mergedWeight', () => {
  const w = { ct: { a: 60 }, cu: { a: 60 }, cm: { a: 60 }, tw: {}, ov: {} }

  it('取第一条被覆盖线路的值', () => {
    const g = lineInputs(CONTRACT, [CN])[0]!
    expect(mergedWeight(w, g, 'a')).toBe(60)
  })

  it('节点在所有被覆盖线路里都没有时给 0', () => {
    const g = lineInputs(CONTRACT, [CN])[0]!
    expect(mergedWeight(w, g, 'nope')).toBe(0)
  })
})

describe('isDivergent', () => {
  const g = lineInputs(CONTRACT, [CN])[0]!

  it('三条线一致时不算分叉', () => {
    expect(isDivergent({ ct: { a: 60 }, cu: { a: 60 }, cm: { a: 60 } }, g)).toBe(false)
  })

  it('三条线不一致时算分叉 —— 保存会把它们拉平，得让人知道', () => {
    expect(isDivergent({ ct: { a: 60 }, cu: { a: 40 }, cm: { a: 60 } }, g)).toBe(true)
  })

  it('单线路组永远不分叉', () => {
    const single = lineInputs(CONTRACT, [TW])[0]!
    expect(isDivergent({ tw: { a: 1 } }, single)).toBe(false)
  })
})

/*
 * `cloudflare_dns`（普通 A / AAAA 轮换）**把五条全合成一条** —— 这一组验的是
 * 上面那些判断在那个形状下同样成立。
 *
 * 它不是「多一条以防万一」：分叉在这一档**几乎必然发生**。灰度上真出过 ——
 * 从 `cloudflare`（LB）换过来的人，库里存的是 LB 那家能表达的形状
 * （只要求 ct/cu/cm 一致，tw 与 ov 各自独立），而纯 DNS 表达不了它。
 * 后端拒绝推送并说「电信 与 台湾 不一致」，**而人在这一页上看不到那件事** ——
 * 除非这个横幅出来。
 */
describe('五条合并成一条（cloudflare_dns）', () => {
  const ALL = {
    code: 'all',
    name: '全部（不分线路）',
    covers: ['ct', 'cu', 'cm', 'tw', 'ov'],
  }
  const g = lineInputs(CONTRACT, [ALL])[0]!

  it('五条都一致：不分叉', () => {
    const w = { ct: { a: 50 }, cu: { a: 50 }, cm: { a: 50 }, tw: { a: 50 }, ov: { a: 50 } }
    expect(isDivergent(w, g)).toBe(false)
  })

  /*
   * **只有末两条不一致也要算分叉。** 从 LB 换过来正是这个形状：前三条被那家
   * 要求一致，后两条各自独立 —— 如果判断只看前几条，这个真实场景恰好逃掉。
   */
  it('前三条一致而 tw / ov 不同：算分叉', () => {
    const w = { ct: { a: 60 }, cu: { a: 60 }, cm: { a: 60 }, tw: { a: 100 }, ov: { a: 40 } }
    expect(isDivergent(w, g)).toBe(true)
  })

  /*
   * **合并框显示第一条被覆盖线路的值，不是平均也不是空。**
   *
   *   平均 → 五条变成一个谁也没配过的数，悄悄改了人的意图
   *   空/0 → 保存之后所有节点退出轮换，那是一次全体摘解析
   *
   * 取第一条至少是他配过的值，而「其余四条会被这个值覆盖」由横幅说出来。
   */
  it('合并框取第一条被覆盖线路的值', () => {
    const w = { ct: { a: 60 }, cu: { a: 30 }, cm: { a: 10 }, tw: { a: 100 }, ov: { a: 40 } }
    expect(mergedWeight(w, g, 'a')).toBe(60)
  })

  /*
   * 某条线路里压根没有这个节点时跳过它，继续往后找 —— 而不是当成 0。
   * 当成 0 的话，一个「只在 ov 配过」的节点会被显示成 0，而人一保存它就真成 0 了。
   */
  it('前面几条没有这个节点时，取第一个有值的', () => {
    const w = { ct: {}, cu: {}, cm: {}, tw: {}, ov: { a: 40 } }
    expect(mergedWeight(w, g, 'a')).toBe(40)
  })
})

describe('isIdle —— 这条解析线路上有没有流量真的被分出去', () => {
  const g: LineInput = { code: 'cn', name: '中国', covers: ['ct', 'cu', 'cm'], supported: true }
  const on = (id: string) => ({ id, dnsEnabled: true })

  it('权重全为 0 时算 idle —— 节点列着，但一条流量都不走', () => {
    // 全新装机就是这个样子：节点在五条线路上都出现，权重 0。
    expect(isIdle({ ct: { a: 0 }, cu: { a: 0 }, cm: { a: 0 } }, g, [on('a')])).toBe(true)
  })

  it('权重表里压根没有这个节点时也算 idle', () => {
    // mergedWeight 对缺失的键返回 0 —— 这正是后端修复前那个闭环的形状：
    // 节点不在权重表里，于是它一份流量也拿不到，而页面不会说这件事。
    expect(isIdle({}, g, [on('a')])).toBe(true)
  })

  it('有一个非 0 权重就不算 idle', () => {
    expect(isIdle({ ct: { a: 0, b: 10 }, cu: {}, cm: {} }, g, [on('a'), on('b')])).toBe(false)
  })

  it('**只数参与解析的节点**：唯一有权重的那台被暂停了，仍然算 idle', () => {
    // 这条是这个函数存在的理由。按「有没有非 0 权重」判会说「不 idle」，
    // 而那台机器不承载流量 —— 这条线路实际上没有出口，界面却不会提示。
    expect(
      isIdle({ ct: { a: 60 }, cu: { a: 60 }, cm: { a: 60 } }, g, [{ id: 'a', dnsEnabled: false }]),
    ).toBe(true)
  })

  it('一个候选节点都没有时不算 idle —— 那是「没有节点」，不是「没分流量」', () => {
    // 两种状态要说不同的话：空线路该说「还没有节点」，
    // 而 idle 说的是「节点在这儿，但你还没给它配权重」。
    expect(isIdle({}, g, [])).toBe(false)
  })
})

describe('computeShares', () => {
  const e = (node: string, enabled: boolean, weight: number) => ({ node, enabled, weight })

  it('按权重算，退出解析的不进分母', () => {
    const m = computeShares([e('a', true, 60), e('b', true, 40), e('c', false, 100)], true)
    expect(m.get('a')).toBe(60)
    expect(m.get('b')).toBe(40)
    expect(m.get('c')).toBe(0)
  })

  /*
   * **这一条是我漏过一次的那半。**
   *
   * 把权重输入框换成「轮换」两个字之后，我以为就说清楚了 —— 而占比条还在按
   * 库里的权重画 60/40。那正是后端当初拒绝默默取等权时担心的：
   * **界面画着 60/40 而实际是轮询，而那种不一致没有任何地方会说出来。**
   *
   * 库里那些权重仍然存着（换回 DNSPod 还要用），但普通 A / AAAA 记录只有
   * 「在或不在」—— 这里画的必须是**将会发生的事**。
   */
  it('表达不了权重时均分，而不是按库里的权重画', () => {
    const m = computeShares([e('a', true, 60), e('b', true, 40), e('c', true, 0)], false)
    expect(m.get('a')).toBe(m.get('b'))
    expect(m.get('b')).toBe(m.get('c'))
    expect(m.get('a')).toBeCloseTo(33.3, 1)
  })

  it('表达不了权重时，退出解析的仍然是 0 —— 均分只在参与的之间', () => {
    const m = computeShares([e('a', true, 60), e('b', true, 40), e('c', false, 100)], false)
    expect(m.get('a')).toBe(50)
    expect(m.get('b')).toBe(50)
    expect(m.get('c')).toBe(0)
  })

  /*
   * 权重全是 0 时不要除以零。按权重算那一支给 0（那是真的：谁都分不到），
   * 而均分那一支照样均分 —— **轮换不看权重，全 0 也照转**。
   */
  it('权重全 0：按权重算是 0，而轮换照样均分', () => {
    const zeros = [e('a', true, 0), e('b', true, 0)]
    expect(computeShares(zeros, true).get('a')).toBe(0)
    expect(computeShares(zeros, false).get('a')).toBe(50)
  })

  it('一个参与的都没有：不炸，全 0', () => {
    const m = computeShares([e('a', false, 60)], false)
    expect(m.get('a')).toBe(0)
  })
})
