import { describe, expect, it } from 'vitest'
import { countChanges, diffLines, foldUnchanged, toLines } from './diff'

const ops = (a: string[], b: string[]) => diffLines(a, b).map((l) => l.op).join(' ')
const texts = (a: string[], b: string[]) => diffLines(a, b).map((l) => `${l.op}:${l.text}`)

describe('diffLines', () => {
  it('完全相同时全是 same', () => {
    expect(ops(['a', 'b'], ['a', 'b'])).toBe('same same')
  })

  it('中间改一行 = 一删一增，前后保持 same', () => {
    expect(texts(['a', 'b', 'c'], ['a', 'B', 'c'])).toEqual([
      'same:a',
      'del:b',
      'add:B',
      'same:c',
    ])
  })

  it('纯新增不会把已有行标成变更', () => {
    expect(ops(['a', 'b'], ['a', 'x', 'b'])).toBe('same add same')
  })

  it('纯删除同理', () => {
    expect(ops(['a', 'x', 'b'], ['a', 'b'])).toBe('same del same')
  })

  it('空 before = 整块新增（version 0 的新建路由就是这种）', () => {
    expect(ops([], ['a', 'b'])).toBe('add add')
  })

  it('空 after = 整块删除 —— 调用方必须自己判断这是不是它想表达的意思', () => {
    // 预览校验失败时后端给的 after 就是空串。直接丢进来会渲染成
    // 「整份配置被删光」，那是严重的误导 —— 所以弹层在 after 为空时不画 diff。
    expect(ops(['a', 'b'], [])).toBe('del del')
  })

  it('行号按各自的序列走，新增行没有 beforeNo', () => {
    const d = diffLines(['a', 'b'], ['a', 'x', 'b'])
    expect(d.map((l) => [l.beforeNo, l.afterNo])).toEqual([
      [1, 1],
      [null, 2],
      [2, 3],
    ])
  })

  it('重排不会被误判成「全部未变更」', () => {
    // LCS 只能保住一边，另一边必然是一删一增 —— 这正是期望行为
    const d = diffLines(['a', 'b'], ['b', 'a'])
    expect(countChanges(d)).toBe(2)
  })
})

describe('foldUnchanged', () => {
  const line = (t: string, op: 'same' | 'add' = 'same') => ({
    op,
    text: t,
    beforeNo: null,
    afterNo: null,
  })

  it('变更点上下 context 行保留，其余折叠', () => {
    const lines = [
      ...Array.from({ length: 10 }, (_, i) => line(`s${i}`)),
      line('new', 'add'),
      ...Array.from({ length: 10 }, (_, i) => line(`t${i}`)),
    ]
    const blocks = foldUnchanged(lines, 3)
    expect(blocks.map((b) => b.kind)).toEqual(['fold', 'lines', 'fold'])
    expect(blocks[0]).toMatchObject({ kind: 'fold', count: 7 })
    // 变更行 + 上下各 3 行
    expect(blocks[1]!.lines).toHaveLength(7)
  })

  it('全部未变更时折成一块', () => {
    const lines = Array.from({ length: 20 }, (_, i) => line(`s${i}`))
    const blocks = foldUnchanged(lines, 3)
    expect(blocks).toHaveLength(1)
    expect(blocks[0]).toMatchObject({ kind: 'fold', count: 20 })
  })

  it('太短的未变更区不折叠 —— 折三行再点一下展开是负收益', () => {
    const lines = [line('a', 'add'), line('b'), line('c'), line('d', 'add')]
    const blocks = foldUnchanged(lines, 1)
    // b/c 落在两个变更行的 context 里，整体不该出现 fold
    expect(blocks.every((b) => b.kind === 'lines')).toBe(true)
  })

  it('折叠块保留原始行，展开时不必重算 diff', () => {
    const lines = Array.from({ length: 12 }, (_, i) => line(`s${i}`))
    lines.push(line('x', 'add'))
    const fold = foldUnchanged(lines, 2).find((b) => b.kind === 'fold')!
    expect(fold.lines).toHaveLength(fold.count)
    expect(fold.lines[0]!.text).toBe('s0')
  })
})

describe('toLines', () => {
  it('空字符串给空数组，而不是一个含空串的数组', () => {
    expect(toLines('')).toEqual([])
  })

  it('保留空行 —— JSON 里的空行也是内容', () => {
    expect(toLines('a\n\nb')).toEqual(['a', '', 'b'])
  })
})
