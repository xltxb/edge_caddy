#!/usr/bin/env node
/**
 * 把 mock 的响应形状与真主控的逐个端点比对。
 *
 * 为什么要有这个：这一轮设置页的四个 bug，三个同一个根——**我的 mock 比真主控
 * 宽松**，于是 dev 下一切正常，真机上才炸。最刺眼的一条是 `PUT /settings`：
 * 真主控回 `data: null`，我的 mock 回完整对象，而界面把返回值当成新状态用了。
 * 保存成功之后整页空白，toast 还说「设置已保存」。
 *
 * `check-premises.mjs` 问的是「后端是不是我以为的那样」，它对着真主控问。
 * 这个脚本问的是另一件事：**「我开发时面对的那个世界，和真实世界形状一样吗」**。
 * 前者能发现「后端变了」，发现不了「我造的替身从一开始就不像」。
 *
 * 比的是**形状不是值**：键集合 + 值的类型。值本来就该不同（种子数据 vs 真库），
 * 键和类型不该。
 *
 *   node scripts/check-shapes.mjs [--base http://localhost:8080/api/v1]
 *                                 [--user fe] [--pass fe-dev-pass]
 *
 * 退出码：0 一致 · 1 有分歧 · 3 连不上真主控（还没轮到，不是失败）
 */

import { setupServer } from 'msw/node'
import { createServer as createViteServer } from 'vite'

const arg = (k, d) => {
  const i = process.argv.indexOf(`--${k}`)
  return i > 0 ? process.argv[i + 1] : d
}
const BASE = arg('base', 'http://localhost:8080/api/v1')
const USER = arg('user', 'fe')
const PASS = arg('pass', 'fe-dev-pass')

/* ── 形状：键集合 + 类型，递归；数组只看第一个元素 ────────────────────── */

function shapeOf(v, depth = 0) {
  if (v === null) return 'null'
  if (Array.isArray(v)) return depth > 6 ? 'array' : `[${v.length ? shapeOf(v[0], depth + 1) : '?'}]`
  if (typeof v === 'object') {
    if (depth > 6) return 'object'
    const keys = Object.keys(v).sort()
    return `{${keys.map((k) => `${k}:${shapeOf(v[k], depth + 1)}`).join(',')}}`
  }
  return typeof v
}

const NOTHING = '（没有这个键）'
const isStructure = (t) => t.startsWith('{') || t.startsWith('[')

/**
 * 一处分歧是**硬**的还是**软**的。
 *
 * 这个区分决定这个检查器有没有用：全报的话，每次种子数据和真库不一样都会红，
 * 红久了就没人看了——**一个总在响的警报等于没有警报**。
 *
 * 软（不报）：`null` 对上一个基本类型，或者 `null` 对上「这个键不存在」。
 *   前者是可空字段两边恰好一有一无，后者是后端 `omitempty` 而 mock 写了 null，
 *   两边都表示「没有值」。分页游标每次都会撞上这一条。
 *
 * 硬（要报）：
 *   - 一边有这个键、另一边压根没有 —— **界面读到 undefined，静默渲染成空**
 *   - `null` 对上一个对象或数组 —— 缺的是结构不是值。`PUT /settings` 那条
 *     （真主控回 `data: null`，mock 回完整对象）正是这一类
 *   - 两个都不是 null 的类型对不上
 */
function isHard(mock, real) {
  const nullish = (t) => t === 'null' || t === NOTHING
  if (nullish(mock) && nullish(real)) return false
  if (nullish(mock)) return isStructure(real) || real === NOTHING ? true : mock === NOTHING
  if (nullish(real)) return isStructure(mock) || mock === NOTHING ? true : real === NOTHING
  return true
}

/**
 * 两个形状的分歧，逐字段列出来。
 *
 * `[?]`（空数组）对上任何数组不算分歧：种子数据里有、真库里空，是常事。
 */
function diffShape(a, b, path = '$', out = []) {
  if (a === b) return out
  const objA = a.startsWith('{')
  const objB = b.startsWith('{')
  const arrA = a.startsWith('[')
  const arrB = b.startsWith('[')

  if (arrA && arrB) {
    const ea = a.slice(1, -1)
    const eb = b.slice(1, -1)
    if (ea === '?' || eb === '?') return out // 一边是空数组，无从比较
    return diffShape(ea, eb, `${path}[]`, out)
  }
  if (!objA || !objB) {
    out.push({ path, mock: a, real: b })
    return out
  }

  const parse = (s) => {
    // 手写切分：值里有嵌套的 {} 和 []，不能按逗号 split
    const fields = {}
    let depth = 0
    let cur = ''
    for (const ch of s.slice(1, -1)) {
      if (ch === '{' || ch === '[') depth++
      if (ch === '}' || ch === ']') depth--
      if (ch === ',' && depth === 0) {
        const i = cur.indexOf(':')
        fields[cur.slice(0, i)] = cur.slice(i + 1)
        cur = ''
        continue
      }
      cur += ch
    }
    if (cur) {
      const i = cur.indexOf(':')
      fields[cur.slice(0, i)] = cur.slice(i + 1)
    }
    return fields
  }

  const fa = parse(a)
  const fb = parse(b)
  for (const k of new Set([...Object.keys(fa), ...Object.keys(fb)])) {
    if (!(k in fa)) out.push({ path: `${path}.${k}`, mock: NOTHING, real: fb[k] })
    else if (!(k in fb)) out.push({ path: `${path}.${k}`, mock: fa[k], real: NOTHING })
    else diffShape(fa[k], fb[k], `${path}.${k}`, out)
  }
  return out
}

/* ── 两个世界 ───────────────────────────────────────────────────────── */

/*
 * MSW 的 handler 里写的是相对路径（`/api/v1/...`），浏览器里靠 `location` 解析成
 * 绝对地址。Node 里没有 `location`，于是**一条都匹配不上** —— 而它的表现是
 * 「没有 handler 处理这个请求」，看起来像 mock 里少写了端点。给它一个 origin。
 */
globalThis.location = new URL('http://localhost/')

/*
 * 用 Vite 加载 mock —— 它是 TS，而且 import 里带 `@/` 别名，Node 直接 import
 * 解析不了。走 Vite 还有一层好处：**加载它的是和 dev server 同一套解析规则**，
 * 否则这个检查器面对的又是第三个世界了。
 */
const vite = await createViteServer({
  server: { middlewareMode: true },
  appType: 'custom',
  logLevel: 'error',
})
const { handlers } = await vite.ssrLoadModule('/mocks/handlers.ts')
const server = setupServer(...handlers)

let realCookie = ''
async function real(path, init = {}) {
  const res = await fetch(BASE + path, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(realCookie ? { cookie: realCookie } : {}),
      ...init.headers,
    },
  })
  const sc = res.headers.get('set-cookie')
  if (sc) realCookie = sc.split(';')[0]
  return { status: res.status, body: await res.json().catch(() => null) }
}

async function mock(path, init = {}) {
  server.listen({ onUnhandledRequest: 'error' })
  try {
    const res = await fetch('http://localhost/api/v1' + path, {
      ...init,
      headers: { 'Content-Type': 'application/json', ...init.headers },
    })
    return { status: res.status, body: await res.json().catch(() => null) }
  } finally {
    server.close()
  }
}

/* ── 先自检：连不上就退 3，不要让下面每一条「没有分歧」都因为空对空而变绿 ── */

const login = await real('/auth/login', {
  method: 'POST',
  body: JSON.stringify({ username: USER, password: PASS }),
}).catch((e) => ({ status: 0, body: null, err: e }))

if (login.status !== 200 || login.body?.code !== 0) {
  console.log(`\n连不上真主控（${BASE}）或登录失败，这个检查还没轮到。`)
  console.log(`  ${login.err ? login.err.message : `HTTP ${login.status} code=${login.body?.code}`}`)
  console.log('\n  它要的是一个跑着的主控 —— 而它检查的正是「我造的替身像不像它」，')
  console.log('  没有真的那一个就无从比较。跳过不等于通过。\n')
  process.exit(3)
}

/*
 * 只对**读**做比对，外加 `PUT /settings` 这一个写。
 *
 * 写端点会改真主控的状态，而这是个检查器不是测试——它不该在别人正用着的
 * 环境里留下痕迹。`PUT /settings` 是例外：它是这一轮出事的那一个，而且可以
 * 发一个空 body（什么都不改）就看到响应形状。
 */
const CASES = [
  { name: 'GET /auth/session', path: '/auth/session' },
  { name: 'GET /overview', path: '/overview' },
  { name: 'GET /settings', path: '/settings' },
  { name: 'GET /certs', path: '/certs' },
  { name: 'GET /deploys', path: '/deploys' },
  { name: 'GET /audit', path: '/audit' },
  { name: 'GET /alerts', path: '/alerts' },
  {
    name: 'PUT /settings（空 body，什么都不改）',
    path: '/settings',
    init: { method: 'PUT', body: '{}' },
  },
  /*
   * `PUT /alerts` 也在这里，而且它是被这张表**漏掉过一次**的那个。
   *
   * 设置页白屏修完之后，告警页还带着一模一样的 bug —— 同样把 `PUT` 的返回值
   * 当成新状态，而真主控同样回 `data: null`。它逃掉不是因为难，是因为这张表
   * 当时只列了出事的那一个。**修完一个 bug，同形状的另一个不会自己浮出来。**
   *
   * 这一条本身也错过一次：原先「先 GET 一份现值再原样写回」，而 `PUT` 的形状
   * 与 `GET` **必然不同**（契约 §12）。mock 改严之后它回 1001、真主控回 0，
   * 而两边的 `data` 都是 `null` —— **形状一样，于是它绿着，什么也没验**。
   * 那次是加了 `code` 比对才看见的。现在发一个真正的空 body：四个字段都可省，
   * 空 body 是个合法的 no-op。
   */
  {
    name: 'PUT /alerts（空 body，什么都不改）',
    path: '/alerts',
    init: { method: 'PUT', body: '{}' },
  },
]

const rows = []
for (const c of CASES) {
  let m, r
  const init = { ...c.init }
  try {
    m = await mock(c.path, init)
  } catch (e) {
    rows.push({ name: c.name, err: `mock 侧失败：${e.message}` })
    continue
  }
  try {
    r = await real(c.path, init)
  } catch (e) {
    rows.push({ name: c.name, err: `真主控侧失败：${e.message}` })
    continue
  }
  /*
   * `code` 比**值**，不比类型。
   *
   * 别处一律只比形状（值本来就该不同），但信封上的 `code` 是个例外：它的值
   * 就是它的含义。mock 回 1001、真主控回 0 —— 两个 `data` 都是 `null`，形状
   * 完全一致，而它们说的是完全相反的两件事。少了这一条，一个「mock 拒了、
   * 真主控收了」的分歧看起来跟一致一模一样。
   */
  const diffs = diffShape(shapeOf(m.body), shapeOf(r.body))
  rows.push({
    name: c.name,
    diffs,
    mockStatus: m.status,
    realStatus: r.status,
    mockCode: m.body?.code,
    realCode: r.body?.code,
  })
}

/* ── 报告 ──────────────────────────────────────────────────────────── */

let bad = 0
console.log('')
for (const row of rows) {
  if (row.err) {
    bad++
    console.log(`✗ ${row.name}\n    ${row.err}`)
    continue
  }
  const codeDiff = row.mockCode !== row.realCode
  const statusDiff = row.mockStatus !== row.realStatus || codeDiff
  const hard = row.diffs.filter((d) => isHard(d.mock, d.real))
  const soft = row.diffs.length - hard.length
  const softNote = soft ? `（另有 ${soft} 处可空字段两边取值不同，不算分歧）` : ''

  if (!hard.length && !statusDiff) {
    console.log(`✓ ${row.name} ${softNote}`)
    continue
  }
  bad++
  console.log(`✗ ${row.name} ${softNote}`)
  if (row.mockStatus !== row.realStatus) {
    console.log(`    HTTP  mock ${row.mockStatus} · 真主控 ${row.realStatus}`)
  }
  if (codeDiff) {
    console.log(`    code  mock ${row.mockCode} · 真主控 ${row.realCode}`)
    console.log(`          —— 一个收下了一个拒了，而两边的 data 形状可能一模一样`)
  }
  for (const d of hard) {
    console.log(`    ${d.path}`)
    console.log(`        mock   ${d.mock}`)
    console.log(`        真主控 ${d.real}`)
  }
}

await vite.close()

console.log('')
if (bad) {
  console.log(`${bad} / ${rows.length} 个端点的形状对不上。`)
  console.log('')
  console.log('**分歧本身不一定是 mock 错了**——也可能是真主控变了而 mock 没跟。')
  console.log('但两种情况下，dev 里看到的都不是发布后会看到的那个东西。')
  console.log('')
  process.exit(1)
}
console.log(`${rows.length} 个端点，mock 与真主控的形状一致。`)
console.log('')
/*
 * 显式退出。MSW 的拦截器和 Vite 的 watcher 会把事件循环挂住 —— 失败分支有
 * `process.exit(1)` 所以一直没暴露，**第一次全绿的时候它就再也不返回了**。
 * 一个只在成功时挂死的检查步骤，会让整条流水线停在「看起来还在跑」。
 */
process.exit(0)
