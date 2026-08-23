import { describe, expect, it } from 'vitest'
import { isDivergent, isIdle, lineInputs, mergedWeight, type LineInput } from './capability'

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
