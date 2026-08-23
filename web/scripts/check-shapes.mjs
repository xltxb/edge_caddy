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
import { readFileSync } from 'node:fs'

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
/** 本轮比对中因为空数组而**没比到内容**的路径。每个端点比对前清空。 */
let empties = []

function diffShape(a, b, path = '$', out = []) {
  if (a === b) return out
  const objA = a.startsWith('{')
  const objB = b.startsWith('{')
  const arrA = a.startsWith('[')
  const arrB = b.startsWith('[')

  if (arrA && arrB) {
    const ea = a.slice(1, -1)
    const eb = b.slice(1, -1)
    if (ea === '?' || eb === '?') {
      /*
       * 一边是空数组，**无从比较** —— 不算分歧是对的（种子有、真库空是常事），
       * 但**要记下来**：这一层里的字段一个都没比到。
       *
       * 不记的话，`✓ GET /certs` 跟一次真的逐字段比过长得一模一样。
       * 而它骗过我一次：我据此说「`not_after` 和 `domains` 跟真实响应对上了」，
       * 实际本地主控的 /certs 返回 0 条，比的是两个空列表。
       *
       * > **一条探测通过了，不代表它验的是你以为的那件事。**
       */
      empties.push(path)
      return out
    }
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
  /*
   * ── 已知缺口：`GET /nodes` 比不了，而它是最该比的那个 ──
   *
   * 节点页是这个控制台最核心的一页，而**它的响应形状从没被比过**。代价是实的：
   * `scope` / `key_type`（前端声明了、后端从来没发过，界面上一直是空格子）
   * 和 `not_after`（后端一直在发、前端不知道）都是在别处偶然发现的 ——
   * 它们本该由这里抓到。
   *
   * 比不了的原因是**这个仓库有两套 mock**：MSW 的 `mocks/handlers.ts`（这个脚本
   * 取的就是它）和 Node 侧的 vite 插件 `mocks/node-mock.ts`。`/nodes` 属于后者，
   * 而这个脚本 `ssrLoadModule('/mocks/handlers.ts')` 拿不到它。加进 CASES 会
   * 直接报「没有匹配的 handler」。
   *
   * **不给 handlers.ts 再写一份 `/nodes`** —— 那就有两份 mock 了，而两份迟早
   * 分叉；到时候这个脚本比的是「MSW 那份和真主控一致」，而界面用的是另一份。
   * 那比不比更坏：它会给出一个关于错误对象的合格证。
   *
   * 真正的修法是让这个脚本走 HTTP 打到 vite dev server（两套 mock 都在那后面），
   * 而不是在进程内装 MSW。没做，**所以这里是一个洞，不是一个决定**。
   */
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

/*
 * **写端点的覆盖账：可以不比，但必须说出为什么。**
 *
 * `PUT /alerts` 带着和设置页一模一样的白屏活了一整轮，原因不是它难，是这张
 * 用例表当时只列了出事的那一个 —— **而「没覆盖」和「覆盖了没问题」在输出里
 * 长得一模一样**。
 *
 * 所以：`request-shapes.json` 里的每个写端点，要么在 CASES 里，要么在这里
 * 写明理由。两样都没有就红 —— 新加的端点默认落进「两样都没有」，逼我当场决定。
 *
 * 这不是「先记着以后做」的清单。多数理由是**做不到**，不是没排上：一个检查器
 * 不该在别人正用着的环境里下线节点、真发一次配置、或者删掉一条规则。
 */
const NOT_COMPARED = {
  'POST /auth/login': '每跑一次都要登录，本来就在自检里走过了',
  'POST /auth/logout': '跑完就没会话了，后面的用例全废',
  'POST /nodes/:id/push': '真给节点推一次配置',
  'POST /nodes/:id/rejoin': '改节点状态',
  'POST /nodes/:id/probe': '会真去连那台机器，慢且结果取决于它在不在',
  'POST /nodes/:id/dns': '改解析，会影响真实流量分配',
  'POST /nodes/:id/drain': '下线一台节点',
  'POST /nodes/token': '签发一次性接入 token，留下一条可用凭证',
  'POST /routes': '会真建一条路由，删不掉（前端没有删路由的路径）',
  'PUT /nodes/:id': '会真改一台节点的元数据；改 public_ip 还会立刻把解析推到服务商',
  'DELETE /nodes/:id': '真删一台节点的记录 —— 删完就没了，而那台机器还在跑',
  'PUT /rules/:id': '要一个真规则 id，而且会动共享密钥',
  'DELETE /rules/:id': '删东西',
  'PUT /drafts/:key': '草稿的字段名按资源种类不同，没有固定形状可比',
  'POST /deploys/preview': '要真实的 res_keys，而且预览结果取决于当前草稿',
  'POST /deploys': '真发一次配置到所有节点',
  'POST /deploys/:cfg/rollback': '真回滚',
  'PUT /dns/weights': '改解析权重，会影响真实流量分配（界面上走得到，f8e84d7 之前走不到）',
  /*
   * 这里**曾经有** `POST /certs/:domain/renew` 与 `POST /certs/renew-check`。
   * 主控不再签发也不再续期（ADR-0015），那两个端点后端删了、前端也不调了 ——
   * 留着理由等于在为一件不会发生的事写理由，跟登记表里那种假话是一回事。
   */
  'POST /alerts/test': '真往 Lark 群里发一张卡片',
}

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
  empties = []
  const diffs = diffShape(shapeOf(m.body), shapeOf(r.body))
  rows.push({
    name: c.name,
    diffs,
    /** 因为空数组而没比到内容的路径 —— 见 diffShape 里那一段。 */
    empties: [...empties],
    mockStatus: m.status,
    realStatus: r.status,
    mockCode: m.body?.code,
    realCode: r.body?.code,
  })
}

/* ── 覆盖账 ─────────────────────────────────────────────────────────── */

const shapes = JSON.parse(readFileSync(new URL('../request-shapes.json', import.meta.url), 'utf8'))
const norm = (k) => k.replace(/:[a-zA-Z_][a-zA-Z0-9_]*/g, ':x')
const comparedWrites = new Set(
  CASES.filter((c) => c.init?.method).map((c) => norm(`${c.init.method} ${c.path}`)),
)
const excused = new Set(Object.keys(NOT_COMPARED).map(norm))

const unaccounted = Object.keys(shapes.endpoints).filter(
  (k) => !comparedWrites.has(norm(k)) && !excused.has(norm(k)),
)

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
  /*
   * **「比过了」和「因为列表是空的所以没比到」必须分开印。**
   *
   * 不分的话两者都是一个 `✓` —— 而它们的含义相反：一个是「这一层逐字段对过」，
   * 一个是「这一层一个字段都没看到」。跟 test.mjs 里那条「一条都没跑不是全过了」
   * 是同一件事，只是粒度更细：这里是**端点通过了，而它的某一层是空转的**。
   */
  const emptyNote = row.empties?.length
    ? `　⚠ ${row.empties.join('、')} 两边都是空数组 —— 这一层的字段一个都没比到`
    : ''

  if (!hard.length && !statusDiff) {
    console.log(`✓ ${row.name} ${softNote}${emptyNote}`)
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
console.log(
  `写端点：比了 ${comparedWrites.size} 个，${excused.size} 个写明了不比的理由` +
    `（跑 \`node scripts/check-shapes.mjs --why\` 看理由）。`,
)
if (process.argv.includes('--why')) {
  for (const [k, why] of Object.entries(NOT_COMPARED)) console.log(`    ${k}\n        ${why}`)
}
if (unaccounted.length) {
  bad += unaccounted.length
  console.log('')
  console.log('✗ 这些写端点既没比、也没写明为什么不比：')
  for (const k of unaccounted) console.log(`    ${k}`)
  console.log('  加进 CASES，或者在 NOT_COMPARED 里写一句理由。')
  console.log('  **两样都没有的时候，它在输出里跟「比过了没问题」长得一样。**')
}

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
