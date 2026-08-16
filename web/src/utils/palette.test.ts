import { describe, it, expect } from 'vitest'
import { suggest, type PaletteNode } from './palette'

const nodes: PaletteNode[] = [
  { id: 'node-hk-01', city: '香港', ip: '103.117.44.18', line: 'CN2 GIA' },
  { id: 'node-us-01', city: '洛杉矶', ip: '194.238.19.62', line: '国际 BGP' },
  { id: 'node-jp-01', city: '东京', ip: '45.32.108.7', line: 'CMIN2' },
]

const ops = (input: string) => suggest(input, nodes).filter((s) => s.kind === 'op')
const navs = (input: string) => suggest(input, nodes).filter((s) => s.kind === 'nav')

describe('命令面板', () => {
  it('单独给动词时列出所有可作用的节点', () => {
    const out = ops('probe')
    expect(out.map((s) => s.node)).toEqual(['node-hk-01', 'node-us-01', 'node-jp-01'])
    expect(out.every((s) => s.verb === 'probe')).toBe(true)
  })

  it('动词 + 节点名精确匹配', () => {
    expect(ops('push node-hk-01').map((s) => s.node)).toEqual(['node-hk-01'])
  })

  // 按城市 / IP / 线路模糊匹配：运维记得住「香港那台」，
  // 未必记得住 node-hk-01 这个 ID。
  it('可按城市、IP、线路模糊匹配', () => {
    expect(ops('probe 香港').map((s) => s.node)).toEqual(['node-hk-01'])
    expect(ops('probe 194.238').map((s) => s.node)).toEqual(['node-us-01'])
    expect(ops('probe CMIN').map((s) => s.node)).toEqual(['node-jp-01'])
  })

  it('大小写不敏感', () => {
    expect(ops('PUSH NODE-HK-01').map((s) => s.node)).toEqual(['node-hk-01'])
  })

  // 空输入列出全部候选，而不是一片空白——面板刚打开时得告诉人能做什么。
  it('空输入列出全部节点操作与页面跳转', () => {
    const all = suggest('', nodes)
    expect(new Set(ops('').map((s) => s.verb))).toEqual(new Set(['push', 'probe', 'drain']))
    // 3 个动词 × 3 台节点，一台都不能漏：空输入是「全集」，不是「示例」
    expect(ops('')).toHaveLength(9)
    expect(navs('').length).toBeGreaterThan(0)
    expect(all).toHaveLength(ops('').length + navs('').length)
  })

  // 跳转命令是命令面板的另一半：⌘K 之后敲「审计」就该能过去。
  it('可按页面名跳转，目标是真实路由', () => {
    const out = navs('审计')
    expect(out).toHaveLength(1)
    expect(out[0].to).toBe('/audit')
  })

  it('无法匹配时返回空，不臆造', () => {
    expect(suggest('push node-does-not-exist', nodes)).toEqual([])
    expect(suggest('飞天遁地', nodes)).toEqual([])
  })

  // drain 会把节点摘出去，敲一行就执行，不标记很容易在快速操作中误伤。
  it('破坏性动词被标记', () => {
    expect(ops('drain node-hk-01')[0].destructive).toBe(true)
    expect(ops('probe node-hk-01')[0].destructive).toBe(false)
    expect(navs('审计')[0].destructive).toBe(false)
  })
})
