/**
 * 输入控件的三态外观 —— 改动 / 非法 / 正常。
 *
 * 这三种着色是工作台的核心可见性：哪个字段被改过、哪个字段填错了，要在
 * 输入框自己身上说清楚。ADR-0012 之所以走字段描述表，就是为了让这段只写一次。
 */
import type { CSSProperties } from 'vue'

export type FieldTone = CSSProperties

export function fieldTone(dirty: boolean, invalid: boolean): FieldTone {
  if (invalid) return { borderColor: 'var(--danger)', background: 'var(--danger-subtle)' }
  if (dirty) return { borderColor: 'var(--accent)', background: 'var(--accent-subtle)' }
  return { borderColor: 'var(--border-default)', background: 'var(--surface-card)' }
}
