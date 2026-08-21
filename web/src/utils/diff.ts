/**
 * 行级 diff —— 工作台右栏的可读表示与确认弹层的权威 diff **共用同一套实现**。
 *
 * 两处喂进来的字节不同（右栏是前端渲染的可读表示，弹层是后端渲染的权威全文），
 * 但折叠交互必须一致，否则同一个人在两个地方看到的「⋯ N 行未变更」行为对不上。
 * 权威性来自「两份都是后端渲染的」，不来自谁算的 diff —— 见 ADR-0007 与契约 §7.1。
 */

export type DiffOp = 'same' | 'add' | 'del'

export interface DiffLine {
  op: DiffOp
  text: string
  /** 在 before 里的行号（1 起）。新增行没有。 */
  beforeNo: number | null
  /** 在 after 里的行号（1 起）。删除行没有。 */
  afterNo: number | null
}

/** 一段连续的 diff 行，或一段被折叠起来的未变更区。 */
export type DiffBlock =
  | { kind: 'lines'; lines: DiffLine[] }
  | { kind: 'fold'; count: number; lines: DiffLine[] }

/**
 * 最长公共子序列，逐行比对。
 *
 * O(n×m) 的朴素 DP。渲染出来的 Caddy 配置是几百行量级，够用；
 * 真到了几千行再说，过早换成 Myers 只会让这段变难读。
 */
export function diffLines(before: string[], after: string[]): DiffLine[] {
  const n = before.length
  const m = after.length

  // lcs[i][j] = before[i..] 与 after[j..] 的最长公共子序列长度
  const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i]![j] = before[i] === after[j] ? lcs[i + 1]![j + 1]! + 1 : Math.max(lcs[i + 1]![j]!, lcs[i]![j + 1]!)
    }
  }

  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (before[i] === after[j]) {
      out.push({ op: 'same', text: before[i]!, beforeNo: i + 1, afterNo: j + 1 })
      i++
      j++
    } else if (lcs[i + 1]![j]! >= lcs[i]![j + 1]!) {
      out.push({ op: 'del', text: before[i]!, beforeNo: i + 1, afterNo: null })
      i++
    } else {
      out.push({ op: 'add', text: after[j]!, beforeNo: null, afterNo: j + 1 })
      j++
    }
  }
  while (i < n) {
    out.push({ op: 'del', text: before[i]!, beforeNo: i + 1, afterNo: null })
    i++
  }
  while (j < m) {
    out.push({ op: 'add', text: after[j]!, beforeNo: null, afterNo: j + 1 })
    j++
  }
  return out
}

/**
 * 把变更点上下 `context` 行之外的未变更区折叠起来。
 *
 * 折叠块保留原始行，展开时不需要重新算 diff。
 * 只有超过 `context * 2 + 1` 行的未变更区才值得折叠 —— 折叠三行然后显示
 * 「⋯ 3 行未变更」既没省地方，还多一次点击。
 */
export function foldUnchanged(lines: DiffLine[], context = 3): DiffBlock[] {
  const changed = lines.map((l) => l.op !== 'same')
  const keep = new Array<boolean>(lines.length).fill(false)
  for (let i = 0; i < lines.length; i++) {
    if (!changed[i]) continue
    for (let k = Math.max(0, i - context); k <= Math.min(lines.length - 1, i + context); k++) {
      keep[k] = true
    }
  }

  const blocks: DiffBlock[] = []
  let i = 0
  while (i < lines.length) {
    if (keep[i]) {
      const start = i
      while (i < lines.length && keep[i]) i++
      blocks.push({ kind: 'lines', lines: lines.slice(start, i) })
    } else {
      const start = i
      while (i < lines.length && !keep[i]) i++
      const chunk = lines.slice(start, i)
      // 太短的折叠不划算，原样显示
      if (chunk.length <= context) blocks.push({ kind: 'lines', lines: chunk })
      else blocks.push({ kind: 'fold', count: chunk.length, lines: chunk })
    }
  }
  return blocks
}

/** 变更行数（新增 + 删除），底栏与资源树用它。 */
export function countChanges(lines: DiffLine[]): number {
  return lines.reduce((n, l) => n + (l.op === 'same' ? 0 : 1), 0)
}

/** 把 JSON 文本切成行，供上面几个函数消费。 */
export function toLines(text: string): string[] {
  return text.length === 0 ? [] : text.split('\n')
}
