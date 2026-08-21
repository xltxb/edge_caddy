#!/usr/bin/env node
/**
 * 用后端行为解释设计的注释，必须挂在**会通知我的东西**上。
 *
 * 判据（来自后端）：写下一个理由时问一句——**它依赖的那个东西，改的时候会不会
 * 有人通知我**。契约会（改它等于改接口），ADR 会（要写 supersedes），
 * CONTEXT.md 会（术语表是共享的）；**对方的内部实现不会**。
 *
 * 挂不上的那些不是不许写，是要把保质期写进句子的语法里：
 * 「这依赖后端当前的 X」读起来自带提醒，「后端会 X」读起来像一条恒真的道理。
 *
 * 这条补的是 `check-comments.mjs` 够不着的那一类。两者分工：
 *
 *   顺序错（复盘占了首句）    → check-comments，靠「有人在写复盘」这个信号
 *   内容过期（首句不再为真）  → **不发信号，没有通用手段**
 *   └ 其中「陈述后端行为」这一支 → 这个脚本，靠「有没有挂在会通知我的东西上」
 *
 * 覆盖的是那一支，不是整类。后端明确说过第二类没有机械手段——这里也只是把它
 * 最常见的一个来源变成可查的，别当成解决了那一类。
 *
 *   node scripts/check-anchors.mjs
 */
import { readFileSync } from 'node:fs'
import { execSync } from 'node:child_process'

// 只查产品代码。mocks / scripts / 测试里描述后端行为是它们的本职。
const files = execSync('git ls-files src', { encoding: 'utf8' })
  .split('\n')
  .filter((f) => /\.(ts|vue)$/.test(f) && !f.includes('.test.'))

const MENTIONS = /(后端|主控)/
/**
 * 三种锚，共同点是**改了会有人/有东西告诉我**：
 *
 *   契约 §N     改它等于改接口，对方会知道
 *   ADR-N       要写 supersedes
 *   点名的检查  会红，**而且它是自动的** —— 三种里最强的一种
 *
 * 第三种是后端补的，而它此前**不可检**：一条「由某个检查守着」的理由，
 * 除非注释里点了那个检查的名字，否则跟没有锚一样。所以处置不是放宽判据，
 * 是把注释改成点名 —— 「有没有锚」于是变成一个可查的信号。
 *
 * 这里认 `check-premises` / `check:premises` / `scripts/xxx.mjs` 这类写法。
 * （后端那次第六处误报是它的正则不认 `adr/0011-…` 这种文件路径写法 ——
 * **词表不匹配实际写法**，所以这里三种写法都认。）
 */
const ANCHORED = /(契约 §|ADR-\d|adr\/\d|CONTEXT\.md|PRD §|check-\w+|check:\w+|scripts\/[\w-]+\.mjs)/
/** 在**用它解释为什么这么写**，而不只是提一句。 */
const JUSTIFY = /(因为|所以|理由|之所以|否则)/
/** 保质期写进语法里的写法，等价于挂上了 —— 它自己会提醒读者去核。 */
const DATED = /(这依赖后端当前|依赖后端当前的|当前的行为|没有流程保护)/

/** 按**块**取注释，不按行 —— 理由常写在多行块里，而契约引用在别的行上。 */
function blocks(src) {
  const lines = src.split('\n')
  const out = []
  for (let i = 0; i < lines.length; i += 1) {
    const t = lines[i].trim()
    const isJs = t.startsWith('/*')
    const isHtml = t.startsWith('<!--')
    if (!isJs && !isHtml) continue
    const end = isJs ? '*/' : '-->'
    const buf = []
    let j = i
    while (j < lines.length && !lines[j].includes(end)) buf.push(lines[j++])
    if (j < lines.length) buf.push(lines[j])
    out.push({ line: i + 1, text: buf.join('\n') })
    i = j
  }
  return out
}

/**
 * 「这一句在用后端行为讲理由」—— 要求两者落在**同一句**里。
 *
 * 第一版只要求落在同一个注释块里，于是 `deploy.ts` 那条被误报了：它讲的是
 * sessionStorage 该用 session 还是 local（纯前端的取舍），块里另一句顺带提了
 * 「下发在主控侧照常进行」。**两句无关，而检查器把它们算作一句。**
 *
 * 这是同一个毛病的第三种尺度：先是「单行看不见多行结构」，然后是「单行看不见
 * 同一块里的别的行」，这次是「整块看不见句子边界」。**每次都是检查的粒度和被
 * 检查对象的结构不匹配**，只是尺度换了一层。
 */
function justifiedByBackend(text) {
  return text
    .split(/[。；\n]/)
    .some((sentence) => MENTIONS.test(sentence) && JUSTIFY.test(sentence))
}

const hits = []
let scanned = 0
for (const f of files) {
  for (const b of blocks(readFileSync(f, 'utf8'))) {
    if (!justifiedByBackend(b.text)) continue
    scanned += 1
    if (!ANCHORED.test(b.text) && !DATED.test(b.text)) {
      const first = b.text
        .split('\n')
        .slice(1)
        .map((l) => l.replace(/^\s*[/*<!-]+\s*/, '').trim())
        .find(Boolean)
      hits.push(`${f}:${b.line}\n    ${(first ?? '').slice(0, 90)}`)
    }
  }
}

/*
 * 双向核对：`check-premises.mjs` 与产品代码里的反向链接对不对得上。
 *
 * 每条前提都标了「依赖处」指向代码，而代码那一侧此前**一句都没说自己被守着**
 * —— 改到那儿的人不会知道有一条前提覆盖它。补上反向链接之后，两边就是两处
 * 知识：脚本里删掉一条，代码里那句「有一条守着」还指着它，而那句话从此是假的。
 *
 * 所以这里查：**产品代码里凡是说「check-premises 里有一条守着」的，
 * 那个文件必须真的出现在某条前提的「依赖处」里。** 反过来不查 —— 一条前提
 * 没有反向链接只是少了个指路牌，不是假话。
 */
{
  const premises = readFileSync('scripts/check-premises.mjs', 'utf8')

  /*
   * **按结构定位，不按特征猜。**
   *
   * 只取 `await check('前提', '依赖处', …)` 的**第二个参数** —— 那是「依赖处」
   * 这个字段本身，不是「这个文件名在脚本里出现过」。
   *
   * 第一版就是按特征写的：`premises.includes(base)`。于是把某条前提的依赖处整个
   * 抹掉、而文件名仍在别处（比如一句注释里）出现时，核对**照样全绿**。
   * 我当时验过它「认得出」，那次之所以红，只是因为我顺手把文件名也一起抹了 ——
   * **探针撞对了，而它验的不是我以为的那件事。**
   *
   * 这是「用特征代替结构」，比粒度不对更靠前一层：粒度不对至少还在找结构。
   * （判据来自后端，它在同一处栽了两版。）
   */
  const deps = [...premises.matchAll(/await check\(\s*\n?\s*'[^']*',\s*\n?\s*'([^']*)'/g)].map(
    (m) => m[1],
  )
  if (deps.length === 0) {
    console.error('✗ 一条前提的「依赖处」都没解析出来 —— 核对的装置坏了\n')
    process.exit(2)
  }

  const claims = []
  for (const f of files) {
    const src = readFileSync(f, 'utf8')
    if (!/check-premises/.test(src)) continue
    claims.push({ f, base: f.split('/').pop() })
  }
  const broken = claims.filter((c) => !deps.some((d) => d.includes(c.base)))
  if (broken.length) {
    console.error(
      `\n✗ ${broken.length} 处反向链接指向的前提已经不在了：\n` +
        broken.map((c) => `    ${c.f} 说自己被 check-premises 守着，而那里没有提到 ${c.base}`).join('\n') +
        '\n',
    )
    process.exit(1)
  }
  if (claims.length === 0) {
    console.error('✗ 一处反向链接都没找到 —— 装置多半坏了\n')
    process.exit(2)
  }
  console.log(`  ${claims.length} 处反向链接都指得到真实的前提`)
}

/*
 * 自检：这是否定断言（「没有挂不上的」），装置坏了会**因为什么都没扫到而变绿**。
 */
if (files.length === 0) {
  console.error('✗ 一个产品源文件都没找到 —— 装置坏了\n')
  process.exit(2)
}
if (scanned === 0) {
  console.error(`✗ ${files.length} 个文件里一个「用后端行为解释设计」的块都没有 —— 装置多半坏了\n`)
  process.exit(2)
}

if (hits.length) {
  console.error(`\n${hits.length} / ${scanned} 处理由挂不上会通知我的东西：\n\n${hits.join('\n')}\n`)
  console.error('  要么改挂契约 / ADR（多半它本来就在那儿），要么把保质期写进句子里。\n')
  process.exit(1)
}
console.log(`  ${scanned} 处用后端行为解释的注释，都挂在契约 / ADR 上或写了保质期`)
