import type { EventKind, NodeStatus } from '@/api/types'

/** 状态徽章：色 + 文字双编码，任何地方都不许只靠颜色区分（PRD §7 可达性）。 */
export interface StatusMeta {
  text: string
  color: string
  bg: string
  dot: string
}

const STATUS: Record<NodeStatus, StatusMeta> = {
  ok: { text: '在线', color: 'var(--success-text)', bg: 'var(--success-subtle)', dot: 'var(--success)' },
  warn: { text: '异常', color: 'var(--warning-text)', bg: 'var(--warning-subtle)', dot: 'var(--warning)' },
  down: { text: '离线', color: 'var(--danger-text)', bg: 'var(--danger-subtle)', dot: 'var(--danger)' },
}

export const statusMeta = (s: NodeStatus): StatusMeta => STATUS[s]

const EVENT_COLOR: Record<EventKind, string> = {
  ok: 'var(--success)',
  warn: 'var(--warning)',
  crit: 'var(--danger)',
  info: 'var(--accent)',
}

export const eventColor = (k: EventKind): string => EVENT_COLOR[k] ?? 'var(--accent)'

/** 12400 → 12.4k，设计稿的写法。 */
export function fmtConns(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
}

/** 心跳年龄：60 秒内给一位小数，超过给分秒。 */
export function fmtHbAge(sec: number): string {
  if (sec < 60) return `${sec.toFixed(1).replace(/\.0$/, '')}s 前`
  const m = Math.floor(sec / 60)
  const s = Math.floor(sec % 60)
  return `${m}m ${s}s 前`
}

/** RFC3339 → HH:MM:SS，事件流与日志用。 */
export function fmtClock(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
