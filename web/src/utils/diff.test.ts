import { describe, it, expect } from 'vitest'
import { diffLines, foldUnchanged } from './diff'

describe('行级 diff', () => {
  it('相同文本没有任何变更行', () => {
    const rows = diffLines('a\nb\nc', 'a\nb\nc')
    expect(rows.every((r) => r.kind === 'same')).toBe(true)
    expect(rows).toHaveLength(3)
  })

  it('标出新增、删除与保留', () => {
    const rows = diffLines('a\nb\nc', 'a\nB\nc')
    const kinds = rows.map((r) => r.kind)
    expect(kinds).toContain('del')
    expect(kinds).toContain('add')
    expect(rows.filter((r) => r.kind === 'same')).toHaveLength(2)
    expect(rows.find((r) => r.kind === 'del')!.text).toBe('b')
    expect(rows.find((r) => r.kind === 'add')!.text).toBe('B')
  })

  // 行号要各自独立：删除行只有旧行号，新增行只有新行号。
  // 混用会让「跳到第 N 行」跳错地方。
  it('新旧行号各自独立', () => {
    const rows = diffLines('a\nb', 'a\nb\nc')
    const added = rows.find((r) => r.kind === 'add')!
    expect(added.newNo).toBe(3)
    expect(added.oldNo).toBeUndefined()
  })
})

describe('未变更折叠', () => {
  const long = Array.from({ length: 30 }, (_, i) => `line-${i}`).join('\n')
  const changed = long.replace('line-15', 'CHANGED')

  it('变更 ±3 行内的上下文保留，其余折叠', () => {
    const items = foldUnchanged(diffLines(long, changed), 3)
    const folds = items.filter((i) => i.kind === 'fold')
    expect(folds.length).toBeGreaterThan(0)
    // 折叠块要说清折了多少行，否则用户不知道点开会看到什么
    expect(folds[0].count).toBeGreaterThan(0)

    const visible = items.filter((i) => i.kind !== 'fold').map((i) => i.text)
    expect(visible).toContain('CHANGED')
    // 紧邻的上下文必须可见
    expect(visible).toContain('line-14')
    expect(visible).toContain('line-16')
    // 远处的必须被折走
    expect(visible).not.toContain('line-0')
  })

  // 全文都没变时整体折成一块，而不是显示 30 行「没变」。
  it('完全没有变更时折成一块', () => {
    const items = foldUnchanged(diffLines(long, long), 3)
    expect(items).toHaveLength(1)
    expect(items[0].kind).toBe('fold')
    expect(items[0].count).toBe(30)
  })

  // 折叠块小于阈值时不值得折——折起来反而多一次点击。
  it('过短的未变更段不折叠', () => {
    const a = 'x\n1\n2\ny'
    const b = 'X\n1\n2\nY'
    const items = foldUnchanged(diffLines(a, b), 3)
    expect(items.some((i) => i.kind === 'fold')).toBe(false)
  })
})
