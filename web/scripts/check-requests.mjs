#!/usr/bin/env node
/**
 * 钉住 `src/api/requests.ts` 的另一头：**源码里每个写请求都必须登记过**。
 *
 * `requests.ts` 自己保证「字段清单与类型一致」——那是类型检查器管的。
 * 它管不了的是**新加了一个端点而忘了登记**：那种情况下清单仍然自洽，只是少了
 * 一整条。而它导给后端的那份 JSON 会因此漏掉一个端点，后端那条
 * 「接受的字段集 ⊇ 前端会发的字段集」就在那个端点上悄悄不成立了。
 *
 * **少一条比错一条难发现**：错的会被比对报出来，少的那条谁都不会提。
 *
 * 顺带禁掉 `Record<string, unknown>` 当 body —— 设置页往后端发只读状态位
 * （`ops_bot_token_configured`）就是它放过去的，后端静默丢弃并回 `code: 0`。
 *
 *   node scripts/check-requests.mjs
 *   node scripts/check-requests.mjs --emit      # 重新生成 web/request-shapes.json
 */

import { existsSync, readFileSync, readdirSync, statSync, writeFileSync, mkdirSync } from 'node:fs'
import { join, dirname, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createServer as createViteServer } from 'vite'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB = join(HERE, '..')
const SRC = join(WEB, 'src')

/* ── 扫源码里的写请求 ───────────────────────────────────────────────── */

/**
 * 去掉注释再扫。
 *
 * 第一次跑就被自己咬了：`requests.ts` 的文档注释里把
 * `const body: Record<string, unknown>` 当反例引用，扫描器当成了真代码。
 *
 * **只剥块注释和整行的 `//`**，不剥行尾的 —— 字符串里有 `http://` 这种东西，
 * 从 `//` 一路剥到行尾会把它拦腰截断，那时扫出来的是别的语言。
 */
function stripComments(text) {
  return text
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
    .split('\n')
    .map((l) => (/^\s*(\/\/|\*|<!--)/.test(l) ? '' : l))
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

/**
 * 把调用点里的路径归一成登记表的键。
 *
 * `\`/nodes/${encodeURIComponent(id)}/dns\`` → `POST /nodes/:id/dns`
 * 插值一律变 `:x` —— 变量叫什么与端点是哪个无关。
 */
function normalize(method, raw) {
  const path = raw
    .replace(/\$\{[^}]*\}/g, ':x')
    .replace(/^['"`]|['"`]$/g, '')
    .replace(/\/:x/g, '/:x')
  return `${method} ${path}`
}

/** 登记表的键也归一，这样 `:id` 和 `:key` 都能对上 `:x`。 */
const keyOf = (k) => k.replace(/:[a-zA-Z_][a-zA-Z0-9_]*/g, ':x')

// putIfAbsent 也要数进来：它是 PUT 的一种（只建不覆盖，契约 §6.2），
// 而**漏掉一条写请求正是这个脚本存在的理由**——少一条比错一条难发现。
// 第一版正则写的是 `(post|put|del)\(`，putIfAbsent 里 put 后面跟的是 I 不是括号，
// 于是它静默地不在射程内，而计数从 25 变成 24 时才被看见。
const CALL = /http\.(post|putIfAbsent|put|del)(?:<[^>]*>)?\(\s*(`[^`]*`|'[^']*'|"[^"]*")\s*(,)?/g

const found = []
for (const file of walk(SRC)) {
  const text = stripComments(readFileSync(file, 'utf8'))
  for (const m of text.matchAll(CALL)) {
    const method = { post: 'POST', put: 'PUT', putIfAbsent: 'PUT', del: 'DELETE' }[m[1]]
    const line = text.slice(0, m.index).split('\n').length
    found.push({
      key: normalize(method, m[2]),
      hasBody: m[3] === ',',
      where: `${relative(WEB, file)}:${line}`,
    })
  }
}

/* ── 自检：一条都没扫到就别往下报「全都登记了」 ────────────────────── */

if (found.length === 0) {
  console.log('\n✗ 一个写请求都没扫到 —— 正则或目录不对，下面任何结论都不作数。\n')
  process.exit(1)
}

/* ── 读登记表 ───────────────────────────────────────────────────────── */

const vite = await createViteServer({
  server: { middlewareMode: true },
  appType: 'custom',
  logLevel: 'error',
})
const { REQUEST_SHAPES, NESTED_SHAPES } = await vite.ssrLoadModule('/src/api/requests.ts')
await vite.close()

const registered = new Map(Object.keys(REQUEST_SHAPES).map((k) => [keyOf(k), k]))

/* ── 比对 ───────────────────────────────────────────────────────────── */

const problems = []
const seen = new Set()

for (const f of found) {
  seen.add(f.key)
  if (!registered.has(f.key)) {
    problems.push({
      kind: '没登记',
      detail: `${f.key}\n      ${f.where}\n      在 src/api/requests.ts 的 REQUEST_SHAPES 里加一条。`,
    })
  }
}

for (const [norm, original] of registered) {
  if (!seen.has(norm)) {
    problems.push({
      kind: '登记了但源码里没人调',
      detail: `${original}\n      要么是端点删了清单没删，要么是我把路径写错了。`,
    })
  }
}

/*
 * 两种会绕过类型的 body 写法。
 *
 * 第二种是告警页那个 bug 教的：`const body: AlertsPutBody = { ...form.value }`
 * **编译通过**——展开一个变量不触发多余属性检查。于是 `form`（`GET` 的形状）
 * 原样发了出去，后端静默丢掉对不上的那半层。**类型标注只在字面量逐字段写出来
 * 的时候才真的管事**，而那正是这条规则要逼出来的写法。
 */
const BAD_BODY = [
  {
    re: /const\s+body\s*:\s*Record<string,\s*unknown>/g,
    kind: '无类型的 body',
    fix: '改成 src/api/requests.ts 里对应的那个接口。\n      设置页往后端发只读状态位就是这么发出去的。',
  },
  {
    re: /const\s+body\s*:\s*\w+\s*=\s*\{\s*\.\.\./g,
    kind: '用展开构造的 body',
    fix: '逐个字段列出来。展开一个变量不触发多余属性检查，类型标注在这里拦不住 ——\n      告警页把 GET 的形状原样发给了 PUT，就是这么过去的。',
  },
]

for (const file of walk(SRC)) {
  const text = stripComments(readFileSync(file, 'utf8'))
  for (const { re, kind, fix } of BAD_BODY) {
    for (const m of text.matchAll(re)) {
      const line = text.slice(0, m.index).split('\n').length
      problems.push({ kind, detail: `${relative(WEB, file)}:${line}\n      ${fix}` })
    }
  }
}

/* ── 导出 ───────────────────────────────────────────────────────────── */

/**
 * 这份 JSON **提交进仓库**（后端的 Go 测试要在没有 node 的情况下读它），
 * 所以它必须有一道过期检查 —— 否则它就是后端担心的那第三份会过期的东西。
 *
 * 默认模式：重新生成一份跟磁盘上的比，不一样就红，并说清怎么修。
 * `--emit`：真写进去。**只有在别的问题都没有时才写** —— 一份从坏状态里
 * 生成出来的清单，比没有更糟。
 */
const OUT = join(WEB, 'request-shapes.json')

function render() {
  return (
    JSON.stringify(
      {
        说明:
          '前端会往每个写端点发的字段名。由 src/api/requests.ts 生成 —— 那份清单与 TS 类型' +
          '由类型检查器钉在一起，改了接口不同步清单编译不过，所以这份 JSON 不会过期。' +
          '只有字段名，没有语义：取值对不对这里管不了。',
        生成自: 'web/scripts/check-requests.mjs --emit',
        endpoints: REQUEST_SHAPES,
        nested: NESTED_SHAPES,
      },
      null,
      2,
    ) + '\n'
  )
}

const fresh = render()
const onDisk = existsSync(OUT) ? readFileSync(OUT, 'utf8') : null

if (process.argv.includes('--emit')) {
  if (problems.length === 0) {
    mkdirSync(dirname(OUT), { recursive: true })
    writeFileSync(OUT, fresh, 'utf8')
    console.log(
      `\n已写入 ${relative(WEB, OUT)}（${Object.keys(REQUEST_SHAPES).length} 个端点，${Object.keys(NESTED_SHAPES).length} 处嵌套）`,
    )
  }
} else if (onDisk === null) {
  problems.push({
    kind: 'request-shapes.json 不在',
    detail: '后端的测试读它。跑 `pnpm gen:requests` 生成。',
  })
} else if (onDisk !== fresh) {
  problems.push({
    kind: 'request-shapes.json 过期了',
    detail: '登记表改了但没重新生成。跑 `pnpm gen:requests`，然后把它一起提交。',
  })
}

/* ── 报告 ───────────────────────────────────────────────────────────── */

console.log('')
if (problems.length === 0) {
  console.log(`✓ ${found.length} 处写请求，${registered.size} 条登记，对得上。`)
  process.exit(0)
}

for (const p of problems) console.log(`✗ ${p.kind}：${p.detail}\n`)
console.log(`${problems.length} 处对不上。`)
process.exit(1)
