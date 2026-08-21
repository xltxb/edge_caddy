#!/usr/bin/env node
/**
 * 模板里不能有会被**字面渲染**的标记。
 *
 * Vue 模板不会因为你写了 `**粗体**` 而报错，它照实把星号显示出来。跟无配置的
 * prettier 用自己的默认值是同一回事：**一个不认识某种标记的渲染器，不会告诉你
 * 它不认识。**
 *
 * 固化成脚本是因为我犯了两次：第一次在 DNS 页，截图才发现；第二次在工作台，
 * 是我手工重跑那段一次性扫描才发现的 —— 而那段扫描当时没留下来。
 * **一个用完就丢的检查，等于只在写它的那一刻生效过一次。**
 *
 *   node scripts/check-templates.mjs
 */
import { readFileSync } from 'node:fs'
import { execSync } from 'node:child_process'

const files = execSync('git ls-files src', { encoding: 'utf8' })
  .split('\n')
  .filter((f) => f.endsWith('.vue'))

/** 会被字面渲染出来的标记。反引号不查 —— 模板字符串里到处都是，误报太多。 */
const MARKS = [
  { re: /\*\*/, why: 'markdown 粗体，会原样显示成星号（用 <b>）' },
  { re: /\[[^\]]+\]\([^)]+\)/, why: 'markdown 链接，会原样显示（用 <a> 或 RouterLink）' },
]

const hits = []
for (const f of files) {
  const src = readFileSync(f, 'utf8')
  const m = /<template>([\s\S]*)<\/template>/.exec(src)
  if (!m) continue
  // 注释里的星号是给人读的，不渲染 —— 必须整块剥掉，只剥起始行会误报
  const tpl = m[1].replace(/<!--[\s\S]*?-->/g, '')
  tpl.split('\n').forEach((line, i) => {
    for (const mark of MARKS) {
      if (mark.re.test(line)) hits.push(`${f}: ${mark.why}\n    ${line.trim().slice(0, 90)}`)
    }
  })
}

/*
 * 自检：这个脚本天然是个否定断言（「模板里没有 X」），装置坏了会**因为什么都
 * 没匹配到而变绿**。所以先证明它确实读到了模板。
 */
if (files.length === 0) {
  console.error('✗ 一个 .vue 都没找到 —— 装置坏了，下面的「没有问题」不作数\n')
  process.exit(2)
}
const withTemplate = files.filter((f) => /<template>/.test(readFileSync(f, 'utf8'))).length
if (withTemplate === 0) {
  console.error(`✗ ${files.length} 个 .vue 里一个 <template> 都没解析出来 —— 装置坏了\n`)
  process.exit(2)
}

if (hits.length) {
  console.error(`\n${hits.length} 处会被字面渲染的标记：\n\n${hits.join('\n')}\n`)
  process.exit(1)
}
console.log(`\n${withTemplate} 个模板，没有会被字面渲染的标记。\n`)
