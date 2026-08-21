/**
 * 命令面板的候选计算 —— 纯函数，与 Vue 无关，因此可以直接测。
 *
 * 解析 `push <cfg> to <node>` / `pause|resume <node>` / `logs <node>`，
 * 以及对节点名 / 城市 / 服务商 / IP / 线路 / 域名的模糊搜索。
 */

import type { EdgeNode } from '@/model'

export type SuggestAct = 'focus' | 'push' | 'pause' | 'resume' | 'goto' | 'hint' | 'error'

export interface Suggestion {
  kind: '节点' | '命令' | '执行' | '域名' | '错误'
  label: string
  hint: string
  act: SuggestAct
  /** 目标节点 id（节点类候选）。 */
  nodeId?: string
  /** 目标路由（域名类候选）—— 直接跳工作台编辑那条路由。 */
  resKey?: string
}

const COMMANDS = ['push', 'pause', 'resume', 'logs'] as const

/** 节点的可搜字段拼在一起做模糊匹配。 */
function haystack(n: EdgeNode): string {
  return `${n.id} ${n.city} ${n.vendor} ${n.ip} ${n.line}`.toLowerCase()
}

function findNode(nodes: EdgeNode[], frag: string): EdgeNode | undefined {
  const f = frag.toLowerCase()
  // 先精确，再前缀，最后包含 —— 「hk」应当先命中 node-hk-01 而不是随便哪个含 hk 的
  return (
    nodes.find((n) => n.id.toLowerCase() === f) ??
    nodes.find((n) => n.id.toLowerCase().startsWith(f)) ??
    nodes.find((n) => haystack(n).includes(f))
  )
}

export interface SuggestInput {
  query: string
  nodes: EdgeNode[]
  domains: { domain: string; upstream: string }[]
  baseline: string
}

export function suggest({ query, nodes, domains, baseline }: SuggestInput): Suggestion[] {
  const q = query.trim()
  const lower = q.toLowerCase()

  // 空查询：先把需要处理的节点顶上来，再给命令提示。
  // 打开面板时最可能想做的事，就是去处理那个正在出问题的节点。
  if (!q) {
    const out: Suggestion[] = nodes
      .filter((n) => n.status !== 'ok')
      .map((n) => ({
        kind: '节点' as const,
        label: n.id,
        hint: n.status === 'down' ? '离线，需处理' : '状态异常',
        act: 'focus' as const,
        nodeId: n.id,
      }))
    out.push(
      { kind: '命令', label: `push ${baseline} to <node>`, hint: '把当前基线重推给某个节点', act: 'hint' },
      { kind: '命令', label: 'pause <node>', hint: '暂停该节点的 DNS 解析', act: 'hint' },
      { kind: '命令', label: 'logs <node>', hint: '展开该节点的 Agent 日志', act: 'hint' },
    )
    return out
  }

  let m = /^push\s+(\S+)\s+to\s+(\S+)$/.exec(lower)
  if (m) {
    const n = findNode(nodes, m[2]!)
    if (!n) return [{ kind: '错误', label: `找不到节点 ${m[2]}`, hint: '换个节点名试试', act: 'error' }]
    return [
      {
        kind: '执行',
        label: `push ${m[1]} → ${n.id}`,
        hint: 'Enter 把当前基线重推给它',
        act: 'push',
        nodeId: n.id,
      },
    ]
  }

  m = /^(pause|resume)\s+(\S+)$/.exec(lower)
  if (m) {
    const n = findNode(nodes, m[2]!)
    if (!n) return [{ kind: '错误', label: `找不到节点 ${m[2]}`, hint: '换个节点名试试', act: 'error' }]
    const want = m[1] === 'resume'
    if (n.dnsEnabled === want) {
      return [
        {
          kind: '错误',
          label: `${n.id} 已经${want ? '在解析中' : '退出解析'}`,
          hint: '状态不会变化',
          act: 'error',
        },
      ]
    }
    return [
      {
        kind: '执行',
        label: `${m[1]} ${n.id}`,
        hint: want ? '恢复该节点的 DNS 解析' : '暂停该节点的 DNS 解析',
        act: m[1] === 'pause' ? 'pause' : 'resume',
        nodeId: n.id,
      },
    ]
  }

  m = /^logs?\s+(\S+)$/.exec(lower)
  if (m) {
    const n = findNode(nodes, m[1]!)
    if (n) {
      return [
        { kind: '执行', label: `展开 ${n.id} 的日志`, hint: 'Enter 打开', act: 'focus', nodeId: n.id },
      ]
    }
  }

  const out: Suggestion[] = []
  for (const n of nodes) {
    if (haystack(n).includes(lower)) {
      out.push({
        kind: '节点',
        label: n.id,
        hint: `${n.city} · ${n.line}`,
        act: 'focus',
        nodeId: n.id,
      })
    }
  }
  for (const d of domains) {
    if (d.domain.toLowerCase().includes(lower)) {
      out.push({
        kind: '域名',
        label: d.domain,
        hint: `→ ${d.upstream}`,
        act: 'goto',
        resKey: `route:${d.domain}`,
      })
    }
  }
  for (const c of COMMANDS) {
    if (c.startsWith(lower)) {
      out.push({ kind: '命令', label: `${c} <node>`, hint: '补全节点名后 Enter', act: 'hint' })
    }
  }
  return out
}

/** 这条候选按 Enter 是否真的会做事。`hint` 与 `error` 只是提示。 */
export function isRunnable(s: Suggestion | undefined): boolean {
  return !!s && s.act !== 'hint' && s.act !== 'error'
}
