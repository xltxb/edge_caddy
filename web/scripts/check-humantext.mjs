#!/usr/bin/env node
/**
 * 界面上给人看的文案里不能有 markdown 标记。
 *
 * `**这样**` 在注释里是强调，渲染到界面上就是四个星号。而**成因不会消失**：
 * 这个仓库的注释大量用 `**…**`，注释和它下面那行文案是同一口气写出来的。
 * 风格不会改，所以这个毛病会反复发生 —— 这是后端在自己那边扫出 9 处之后的
 * 判断，我这边一字不差地适用。
 *
 * 我自己就有一处：`participation.ts` 里那句 `**先去修那台机器**` 进了
 * `title=`，鼠标悬停时四个星号原样露在提示里。
 *
 * **不加 markdown 解析器去吸收它**：那样星号就成了一个没人声明过的特性，
 * 下一个写文案的人不知道自己在写 markdown，写出 `50% * 2` 会变成斜体。
 *
 * ## 判据
 *
 * 只看**会到界面上**的两种地方：
 *   1. `.vue` 模板里的裸文本（不含 `{{ }}` 插值内部）
 *   2. `.ts` / `.vue` 脚本里的字符串字面量
 *
 * ## 盲区（照规矩写出来，免得它看起来比实际严）
 *
 * - **拼接出来的标记看不见**：`'**' + x + '**'` 不报。
 * - **后端来的文案不在管辖内**：`reason` / `notes` / `detail` 这些是主控
 *   产生、前端原样显示的，星号要在后端去掉（他们有 `scripts/humantext.py`）。
 * - **测试名不算**：`it('**只数参与解析的**…')` 只有开发看得到。
 * - 注释整段剥掉 —— 注释里用 `**` 是这个仓库的风格，不是问题。
 */

import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, dirname, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const WEB = join(dirname(fileURLToPath(import.meta.url)), '..')
const SRC = join(WEB, 'src')

/**
 * 剥注释。三种都要剥，**而且 HTML 注释要整块剥**。
 *
 * 第一版只把「整行以 `<!--` 开头」的行清掉，于是多行 HTML 注释的中间几行
 * 留了下来 —— 11 处报告里 8 处是我自己写在 `<!-- -->` 里的强调。
 * **一个八成是误报的检查器，第二次就没人看了**，所以这一步比判据本身更要紧。
 *
 * 行尾的 `//` 不剥：从它一路剥到行尾会把字符串里的 `http://` 拦腰截断，
 * 那时扫的是另一种语言（check-requests 上踩过同一个）。
 */
function stripComments(text) {
  const blank = (m) => m.replace(/[^\n]/g, ' ')
  return text
    .replace(/\/\*[\s\S]*?\*\//g, blank)
    .replace(/<!--[\s\S]*?-->/g, blank)
    .split('\n')
    .map((l) => (/^\s*(\/\/|\*)/.test(l) ? '' : l))
    .join('\n')
}

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (/\.(ts|vue)$/.test(p) && !/\.(test|spec)\.ts$/.test(p)) out.push(p)
  }
  return out
}

const MARK = /\*\*|__/

/** 字符串字面量。三种引号都要，因为文案里有中文引号、也有模板串。 */
const LITERAL = /'([^'\\\n]|\\.)*'|"([^"\\\n]|\\.)*"|`([^`\\]|\\.)*`/g

const findings = []
let scanned = 0
let literals = 0

for (const file of walk(SRC)) {
  const raw = readFileSync(file, 'utf8')
  const text = stripComments(raw)
  scanned++

  for (const m of text.matchAll(LITERAL)) {
    literals++
    const body = m[0].slice(1, -1)
    if (!MARK.test(body)) continue
    // `__test` 这类标识符不是 markdown：下划线两侧要有可见字符才算标记
    if (!/\*\*/.test(body) && !/__\S+__/.test(body)) continue
    findings.push({
      file,
      line: text.slice(0, m.index).split('\n').length,
      what: '字符串',
      text: body.length > 90 ? body.slice(0, 90) + '…' : body,
    })
  }

  // .vue 模板里的裸文本
  if (file.endsWith('.vue')) {
    const tpl = /<template>([\s\S]*?)<\/template>/.exec(text)
    if (tpl) {
      const inner = tpl[1].replace(/\{\{[\s\S]*?\}\}/g, ' ').replace(/<[^>]*>/g, '\n')
      for (const [i, line] of inner.split('\n').entries()) {
        if (MARK.test(line) && line.trim()) {
          findings.push({ file, line: i + 1, what: '模板文本', text: line.trim().slice(0, 90) })
        }
      }
    }
  }
}

/* ── 自检：一个字符串都没扫到就别报「干净」 ─────────────────────────── */

if (scanned === 0 || literals === 0) {
  console.log(`\n✗ 扫了 ${scanned} 个文件、${literals} 个字符串 —— 装置没工作，下面的结论不作数。\n`)
  process.exit(1)
}

console.log('')
if (findings.length === 0) {
  console.log(`✓ ${scanned} 个文件、${literals} 个字符串，界面文案里没有 markdown 标记。`)
  console.log('')
  process.exit(0)
}

for (const f of findings) {
  console.log(`✗ ${relative(WEB, f.file)}:${f.line}（${f.what}）`)
  console.log(`    ${f.text}`)
}
console.log('')
console.log(`${findings.length} 处界面文案里带着 markdown 标记，它们会原样显示成星号。`)
console.log('把标记去掉 —— **不要**在前端加解析器，那会让星号变成一个没人声明过的特性。')
console.log('')
process.exit(1)
