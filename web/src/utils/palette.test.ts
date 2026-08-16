import { describe, it, expect } from 'vitest'
import { suggest, type PaletteNode, type PaletteRoute } from './palette'

const nodes: PaletteNode[] = [
  { id: 'node-hk-01', city: '香港', ip: '103.117.44.18', line: 'CN2 GIA' },
  { id: 'node-us-01', city: '洛杉矶', ip: '194.238.19.62', line: '国际 BGP' },
  { id: 'node-jp-01', city: '东京', ip: '45.32.108.7', line: 'CMIN2' },
]

const routes: PaletteRoute[] = [
  { domain: 'api.example.com' },
  { domain: 'shop.example.com' },
  { domain: 'static.cdn.internal' },
]

const all = (input: string) => suggest(input, nodes, routes)
const ops = (input: string) => all(input).filter((s) => s.kind === 'op')
const navs = (input: string) => all(input).filter((s) => s.kind === 'nav')
const rts = (input: string) => all(input).filter((s) => s.kind === 'route')

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
    expect(new Set(ops('').map((s) => s.verb))).toEqual(new Set(['push', 'probe', 'drain']))
    // 3 个动词 × 3 台节点，一台都不能漏：空输入是「全集」，不是「示例」
    expect(ops('')).toHaveLength(9)
    expect(navs('').length).toBeGreaterThan(0)
    expect(all('')).toHaveLength(ops('').length + navs('').length + rts('').length)
  })

  // 跳转命令是命令面板的另一半：⌘K 之后敲「审计」就该能过去。
  it('可按页面名跳转，目标是真实路由', () => {
    const out = navs('审计')
    expect(out).toHaveLength(1)
    expect(out[0].to).toBe('/audit')
  })

  it('无法匹配时返回空，不臆造', () => {
    expect(all('push node-does-not-exist')).toEqual([])
    expect(all('飞天遁地')).toEqual([])
  })

  // drain 会把节点摘出去，敲一行就执行，不标记很容易在快速操作中误伤。
  it('破坏性动词被标记', () => {
    expect(ops('drain node-hk-01')[0].destructive).toBe(true)
    expect(ops('probe node-hk-01')[0].destructive).toBe(false)
    expect(navs('审计')[0].destructive).toBe(false)
  })
})

// 域名匹配到的是**路由**，不是节点。
//
// 按域名筛节点没有意义：域名是全网下发的，每台边缘节点都承载全部域名，
// 筛出来永远是全部。人敲域名时想去的是那条路由的配置。
describe('按域名匹配路由', () => {
  it('域名片段列出匹配的路由，跳向工作台条目', () => {
    const out = rts('api.example')
    expect(out).toHaveLength(1)
    expect(out[0].domain).toBe('api.example.com')
    expect(out[0].to).toBe('/workbench/route%3Aapi.example.com')
    expect(out[0].label).toContain('路由')
  })

  it('片段命中多条时都列出来', () => {
    expect(rts('example.com').map((s) => s.domain)).toEqual(['api.example.com', 'shop.example.com'])
  })

  it('大小写不敏感', () => {
    expect(rts('API.EXAMPLE').map((s) => s.domain)).toEqual(['api.example.com'])
  })

  // 节点操作候选与路由候选必须能区分开，否则 ↓↓Enter 会误点。
  it('路由候选与节点操作候选类型不同', () => {
    const mixed = all('example')
    expect(mixed.some((s) => s.kind === 'route')).toBe(true)
    expect(mixed.every((s) => s.kind !== 'op')).toBe(true)
  })

  it('空输入时路由也在全集里', () => {
    expect(rts('').map((s) => s.domain)).toEqual([
      'api.example.com', 'shop.example.com', 'static.cdn.internal',
    ])
  })

  it('匹配不到时不臆造', () => {
    expect(rts('不存在的域名')).toEqual([])
    expect(all('不存在的域名')).toEqual([])
  })

  // 动词开头时不去匹配域名：`push api.example.com` 是「把配置推给某节点」，
  // 不该顺带冒出一条「前往路由」。
  it('动词开头时不混入路由候选', () => {
    expect(all('push api.example.com').filter((s) => s.kind === 'route')).toEqual([])
  })

  it('没有路由时不报错', () => {
    expect(suggest('example', nodes, [])).toEqual([])
  })
})
