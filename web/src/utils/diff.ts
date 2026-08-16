/** 一行 diff。 */
export interface DiffRow {
  kind: 'same' | 'add' | 'del'
  text: string
  /** 旧文本里的行号；新增行没有 */
  oldNo?: number
  /** 新文本里的行号；删除行没有 */
  newNo?: number
}

/** 折叠后的一项：要么是一行，要么是一个「⋯ N 行未变更」的折叠块。 */
export type DiffItem = DiffRow | { kind: 'fold'; count: number; text?: undefined }

/**
 * diffLines 做行级 LCS 差异。
 *
 * 用 LCS 而不是逐行对齐：逐行对齐在插入一行后会把后面所有行都标成变更，
 * 而配置文件里插一行是最常见的改动——那样的 diff 没人看得下去。
 */
export function diffLines(oldText: string, newText: string): DiffRow[] {
  const a = oldText.split('\n')
  const b = newText.split('\n')

  // LCS 长度表
  const lcs: number[][] = Array.from({ length: a.length + 1 }, () => new Array(b.length + 1).fill(0))
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1])
    }
  }

  const rows: DiffRow[] = []
  let i = 0
  let j = 0
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      rows.push({ kind: 'same', text: a[i], oldNo: i + 1, newNo: j + 1 })
      i++
      j++
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      rows.push({ kind: 'del', text: a[i], oldNo: i + 1 })
      i++
    } else {
      rows.push({ kind: 'add', text: b[j], newNo: j + 1 })
      j++
    }
  }
  while (i < a.length) rows.push({ kind: 'del', text: a[i], oldNo: ++i })
  while (j < b.length) rows.push({ kind: 'add', text: b[j], newNo: ++j })
  return rows
}

/**
 * foldUnchanged 把远离变更的未变更行折叠起来（前端文档 §5.1：变更 ±context 行外折叠）。
 *
 * 折叠块要带上行数：不说折了多少行，用户不知道点开会看到什么。
 * 短于 context*2 的段不折——折起来反而多一次点击。
 */
export function foldUnchanged(rows: DiffRow[], context = 3): DiffItem[] {
  const keep = new Array(rows.length).fill(false)
  rows.forEach((r, idx) => {
    if (r.kind === 'same') return
    for (let k = Math.max(0, idx - context); k <= Math.min(rows.length - 1, idx + context); k++) {
      keep[k] = true
    }
  })

  const out: DiffItem[] = []
  let run = 0
  const flush = () => {
    if (run > 0) {
      out.push({ kind: 'fold', count: run })
      run = 0
    }
  }
  for (let idx = 0; idx < rows.length; idx++) {
    if (keep[idx]) {
      flush()
      out.push(rows[idx])
    } else {
      run++
    }
  }
  flush()
  return out
}
