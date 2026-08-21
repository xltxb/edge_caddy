import { describe, expect, it } from 'vitest'
import { isRunnable, suggest, type Suggestion } from './suggest'
import type { EdgeNode } from '@/model'

const node = (id: string, over: Partial<EdgeNode> = {}): EdgeNode => ({
  id,
  city: '香港',
  vendor: 'DMIT',
  line: 'CN2 GIA',
  ip: '203.0.113.7',
  status: 'ok',
  cpu: 10,
  mem: 20,
  conns: 100,
  cpuSeries: [],
  hbAgeMs: 0,
  hbStampedAt: 0,
  cfgVersion: 'cfg-a',
  drift: false,
  dnsEnabled: true,
  drainedAt: null,
  routes: 1,
  rules: 1,
  ...over,
})

const NODES = [
  node('node-hk-01'),
  node('node-jp-01', { city: '东京', vendor: 'V.PS', ip: '45.32.108.7', line: 'CMIN2' }),
  node('node-us-01', { city: '洛杉矶', status: 'down', dnsEnabled: false, ip: '194.238.19.62' }),
]

const DOMAINS = [{ domain: 'api.example.com', upstream: '10.8.0.2:8080' }]

const run = (query: string): Suggestion[] =>
  suggest({ query, nodes: NODES, domains: DOMAINS, baseline: 'cfg-a' })

describe('空查询', () => {
  it('先列出需要处理的节点 —— 打开面板时最可能想做的就是去处理它', () => {
    const out = run('')
    expect(out[0]).toMatchObject({ kind: '节点', label: 'node-us-01', act: 'focus' })
    expect(out[0]!.hint).toContain('离线')
  })

  it('正常的节点不占位置', () => {
    expect(run('').filter((s) => s.kind === '节点')).toHaveLength(1)
  })

  it('随后给命令提示，且提示本身不可执行', () => {
    const cmds = run('').filter((s) => s.kind === '命令')
    expect(cmds.length).toBeGreaterThan(0)
    expect(cmds.every((c) => !isRunnable(c))).toBe(true)
  })
})

describe('push <cfg> to <node>', () => {
  it('解析出可执行的候选', () => {
    const out = run('push cfg-a to hk')
    expect(out).toHaveLength(1)
    expect(out[0]).toMatchObject({ act: 'push', nodeId: 'node-hk-01' })
    expect(isRunnable(out[0])).toBe(true)
  })

  it('找不到节点时明说，而不是给一个空列表', () => {
    // 空列表会被读成「还在输入中」，错误提示才说得出「这个名字不对」
    const out = run('push cfg-a to nope')
    expect(out[0]).toMatchObject({ kind: '错误', act: 'error' })
    expect(isRunnable(out[0])).toBe(false)
  })
})

describe('pause / resume', () => {
  it('pause 一个正在解析的节点是可执行的', () => {
    expect(run('pause hk')[0]).toMatchObject({ act: 'pause', nodeId: 'node-hk-01' })
  })

  it('状态已经如此时不给可执行候选 —— 按下去什么也不会变', () => {
    // node-us-01 已经退出解析，再 pause 一次是空操作
    const out = run('pause us')
    expect(out[0]!.kind).toBe('错误')
    expect(out[0]!.label).toContain('已经')
    expect(isRunnable(out[0])).toBe(false)
  })

  it('resume 一个已退出解析的节点可执行', () => {
    expect(run('resume us')[0]).toMatchObject({ act: 'resume', nodeId: 'node-us-01' })
  })
})

describe('logs', () => {
  it('log 与 logs 都认', () => {
    expect(run('logs jp')[0]).toMatchObject({ act: 'focus', nodeId: 'node-jp-01' })
    expect(run('log jp')[0]).toMatchObject({ act: 'focus', nodeId: 'node-jp-01' })
  })
})

describe('模糊搜索', () => {
  it('按城市搜得到', () => {
    expect(run('东京')[0]).toMatchObject({ label: 'node-jp-01' })
  })

  it('按 IP 搜得到', () => {
    expect(run('194.238')[0]).toMatchObject({ label: 'node-us-01' })
  })

  it('按线路搜得到', () => {
    expect(run('CMIN2')[0]).toMatchObject({ label: 'node-jp-01' })
  })

  it('按域名搜得到，且跳的是工作台里那条路由', () => {
    const hit = run('api.example')[0]!
    expect(hit).toMatchObject({ kind: '域名', act: 'goto', resKey: 'route:api.example.com' })
  })

  it('命令前缀补全', () => {
    const out = run('pau')
    expect(out.some((s) => s.kind === '命令' && s.label.startsWith('pause'))).toBe(true)
  })
})

describe('findNode 的优先级', () => {
  it('精确 > 前缀 > 包含 —— 「hk」应当命中 node-hk-01', () => {
    expect(run('pause hk')[0]!.nodeId).toBe('node-hk-01')
  })

  it('完整 id 精确命中', () => {
    expect(run('logs node-jp-01')[0]!.nodeId).toBe('node-jp-01')
  })
})
