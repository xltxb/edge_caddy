#!/usr/bin/env node
/**
 * 含「改正叙述」的注释块，**首句必须讲现在，不能讲被改掉的那件事**。
 *
 * 为什么单挑这一类：这一轮我往注释里塞了大量「原先是…后来改成…」的复盘，
 * 而**一个从上往下读的人，第一句读到什么就先信什么**。首句留着旧说法的话，
 * 那条注释在最显眼的位置说了一句已经不成立的话——历史本身有价值，
 * 但它该在结论后面，不该在结论前面。
 *
 * 这个形状是后端撞出来的，比「注释过期」还隐蔽一档：它那次 `PeekEnrollToken`
 * 的注释头三行是 `ConsumeEnrollToken` 的，写着「原子地把 Token 标记为已用」
 * ——**而那正是 Peek 明确不做的事**。过期的注释描述的是曾经为真的行为，
 * 那一句从写下的那一刻起就是错的。
 *
 * **只查含改正词的块**，不查所有注释。后端试过「注释首行函数名对不上」，
 * 测试文件里七处全是误报（中文注释不以函数名开头），它的结论是
 * 「做过一次、说清结论，比留下一个会被忽略的检查好」。这一条留着，是因为它
 * 只在我确实写了复盘的地方开火 —— 命中率验过：6 处里 5 处是真的。
 *
 *   node scripts/check-comments.mjs
 */
import { readFileSync } from 'node:fs'
import { execSync } from 'node:child_process'

const files = execSync('git ls-files src mocks scripts', { encoding: 'utf8' })
  .split('\n')
  .filter((f) => /\.(ts|vue|mjs)$/.test(f))

/** 「这段在讲一次改正」的信号词。 */
const CORRECTION = /(原先(?!的)|早先|第一版|曾经写|失效了|我错|打偏)/

/*
 * 词表里**去掉了「是假的」**。
 *
 * 它撞上了一句正常措辞：「格式正确而**意思是假的**值」—— 那讲的是现在，
 * 不是一次改正。两处误报，而它们的首句恰恰是我刚提上去的结论。
 *
 * 这跟我第一遍按行扫注释、后端那条 `Requires=caddy.service` 是同一族：
 * **检查器的词表来自我的习惯，而习惯里的词也会出现在别的意思上。**
 * 一个词能不能进词表，要看它**只**在那个意思上出现。
 */

const hits = []
let scanned = 0
for (const f of files) {
  const lines = readFileSync(f, 'utf8').split('\n')
  for (let i = 0; i < lines.length; i += 1) {
    const t = lines[i].trim()
    if (t !== '/*' && !t.startsWith('/**')) continue
    const block = []
    let j = i
    while (j < lines.length && !lines[j].includes('*/')) block.push(lines[j++])
    if (j < lines.length) block.push(lines[j])
    i = j
    const text = block.join('\n')
    if (!CORRECTION.test(text)) continue
    scanned += 1
    const first =
      block.slice(1).map((l) => l.replace(/^\s*[/*]+\s*/, '').trim()).find(Boolean) ?? ''
    if (CORRECTION.test(first)) {
      hits.push(`${f}:${i - block.length + 2}\n    首句讲的是被改掉的那件事：${first.slice(0, 80)}`)
    }
  }
}

/*
 * 自检：这条是否定断言（「首句不讲旧事」），装置坏了会**因为什么都没扫到而
 * 变绿**。所以先证明它确实找到了含改正叙述的块。
 */
if (files.length === 0) {
  console.error('✗ 一个源文件都没找到 —— 装置坏了\n')
  process.exit(2)
}
if (scanned === 0) {
  console.error(`✗ ${files.length} 个文件里一个「含改正叙述」的块都没找到 —— 装置多半坏了\n`)
  process.exit(2)
}

if (hits.length) {
  console.error(`\n${hits.length} / ${scanned} 个复盘块的首句在讲旧事：\n\n${hits.join('\n')}\n`)
  console.error('  把结论提到首句，历史放它后面。\n')
  process.exit(1)
}
console.log(`\n${scanned} 个复盘块，首句都在讲现在。\n`)
