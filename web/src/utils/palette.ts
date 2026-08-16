import type { NodeVerb } from '@/stores/nodes'

/** 命令面板可作用的节点信息。 */
export interface PaletteNode {
  id: string
  city: string
  ip: string
  line: string
}

/** 节点操作候选。执行时走 nodes store 的 runOp，与行内按钮同一条路径。 */
export interface OpSuggestion {
  kind: 'op'
  verb: NodeVerb
  node: string
  label: string
  destructive: boolean
}

/** 页面跳转候选。to 必须是 router 里真实存在的路径。 */
export interface NavSuggestion {
  kind: 'nav'
  to: string
  label: string
  destructive: false
}

export type Suggestion = OpSuggestion | NavSuggestion

const VERBS: { verb: NodeVerb; label: string; destructive: boolean }[] = [
  { verb: 'push', label: '重推配置', destructive: false },
  { verb: 'probe', label: '探活', destructive: false },
  // drain 会把节点摘出去。标出来是因为面板里敲一行就执行，没有二次确认的余地。
  { verb: 'drain', label: '下线', destructive: true },
]

/** 跳转目标取自 router/index.ts，改路由时这里要同步。 */
const PAGES: { to: string; label: string }[] = [
  { to: '/overview', label: '集群总览' },
  { to: '/nodes', label: '边缘节点' },
  { to: '/workbench', label: '配置工作台' },
  { to: '/routes', label: '反代路由' },
  { to: '/acl', label: '访问控制' },
  { to: '/deploys', label: '下发记录' },
  { to: '/audit', label: '审计日志' },
]

/**
 * suggest 把一行输入解析成候选命令。
 *
 * 节点操作支持按节点名、城市、IP、线路模糊匹配——运维记得住「香港那台」，
 * 未必记得住 node-hk-01 这个 ID。
 */
export function suggest(input: string, nodes: PaletteNode[]): Suggestion[] {
  const q = input.trim().toLowerCase()

  // 空输入给全集而不是示例：面板刚打开时，人要的是「我能做什么」的完整清单
  if (q === '') {
    return [...allOps(nodes), ...PAGES.map(nav)]
  }

  const [head, ...rest] = q.split(/\s+/)
  const v = VERBS.find((x) => x.verb === head)
  if (v) {
    const needle = rest.join(' ')
    const hits = needle === '' ? nodes : nodes.filter((n) => matches(n, needle))
    return hits.map((n) => op(v, n))
  }
  return PAGES.filter((p) => p.label.toLowerCase().includes(q) || p.to.includes(q)).map(nav)
}

function allOps(nodes: PaletteNode[]): OpSuggestion[] {
  return VERBS.flatMap((v) => nodes.map((n) => op(v, n)))
}

function matches(n: PaletteNode, needle: string): boolean {
  return [n.id, n.city, n.ip, n.line].some((f) => f.toLowerCase().includes(needle))
}

function op(v: (typeof VERBS)[number], n: PaletteNode): OpSuggestion {
  return { kind: 'op', verb: v.verb, node: n.id, label: `${v.label} ${n.id}`, destructive: v.destructive }
}

function nav(p: { to: string; label: string }): NavSuggestion {
  return { kind: 'nav', to: p.to, label: `前往 ${p.label}`, destructive: false }
}
