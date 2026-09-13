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
/** 这一轮被显式跳过的路径 —— 报告里要说出来，不能悄悄少比一块。 */
let skipped = []

/**
 * 这一轮声明为「可空」的路径 —— 真主控在这里回 `null` 不算分歧。
 *
 * **但它也不算比过了。** 一边 null 一边有结构，这一层的字段一个都没比到 ——
 * 跟「两边都是空数组」是同一种盲区，所以走同一个 `empties` 报告。
 */
let nullables = new Set()

/**
 * **按 type 变形的那一层，不能拿「第一个元素」当代表。**
 *
 * 访问规则是个异构数组：`ip_whitelist` 的 spec 只有 `ips`，`service_secret`
 * 有 header / algo / ttl_s。两边各取第一条来比，比的是**哪种规则碰巧排在最前**
 * —— 那不是形状检查，是巧合检查，而它每次都会报一堆假分歧。
 *
 * 跳过的是 `spec` 那一层，`items[]` 的其余字段（id / type / enabled /
 * apply_to / version）照比 —— 那些是同构的，也是这个端点主要的形状。
 *
 * **这是个已知的、说出来的缺口**：spec 的形状目前没有任何一处在比。
 * 要补的话得按 type 分组各比一次，那要脚本理解规则的结构 —— 还没做。
 *
 * ## `$.data.incomplete` 是另一种：**键就是数据**
 *
 * 它是 `ruleId → 问题清单` 的 map，而两边的规则本来就不是同一批
 * （mock 有 `scanner-block`，真主控有 `svc-key-1`）。这个脚本把对象的键
 * 当字段名比 —— 在这里它比的是**哪些规则 id 恰好两边都有**，那是巧合不是形状。
 *
 * 代价说在明处：`{res_key, field, reason}` 这个形状**目前没有任何一处在比**。
 * 真要比得按「取任意一条非空的 issue」来，而那要脚本知道哪一层是 map、
 * 哪一层是记录 —— 还没做。
 */
const SKIP_PATHS = new Map([
  ['$.data.items[].spec', '按 type 变形'],
  ['$.data.incomplete', '键就是规则 id，两边不是同一批规则'],
])

function diffShape(a, b, path = '$', out = []) {
  if (SKIP_PATHS.has(path)) {
    /*
     * **理由跟着路径走，不共用一句。**
     *
     * 这两条跳过的性质不同：一条是「同一个键在不同 type 下装着不同东西」，
     * 另一条是「键本身就是数据」。共用一句「按 type 变形」，
     * 套在后一条上**是一句假话** —— 而它正好出现在一个说「这里没比到」
     * 的位置上，读的人没有理由怀疑它。
     *
     * 同样的错我在这个脚本里一小时前刚犯过一次（`empties` 那两个来源）。
     */
    skipped.push({ path, why: SKIP_PATHS.get(path) })
    return out
  }
  if (a === b) return out
  const objA = a.startsWith('{')
  const objB = b.startsWith('{')
  const arrA = a.startsWith('[')
  const arrB = b.startsWith('[')

  /*
   * **契约上就可空的字段，一边 null 一边有数据，不算形状分歧。**
   *
   * `dns_sync.targets`（没同步过）和 `cpu_series`（主控刚重启）都是这种：
   * mock 造了数据是对的 —— 界面要展示那个场景；真主控回 null 也是对的。
   *
   * 但**记进 `empties`**：这一层里的字段一个都没比到，跟「两边都是空数组」
   * 同一种盲区。不记的话，一个豁免过的端点跟一次真的逐字段比过长得一样。
   */
  if (nullables.has(path) && (a === 'null' || b === 'null') && a !== b) {
    empties.push({ path, why: '一边是 null —— 真主控此刻没有这个数据' })
    return out
  }

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
      empties.push({ path, why: '两边都是空数组' })
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
 * 起一个**真的** dev server，而不是在进程内单独装 MSW。
 *
 * ## 为什么是真的起
 *
 * 这个仓库有两套 mock：MSW 的 `mocks/handlers.ts` 和 Node 侧的 vite 插件
 * （`mocks/node-mock.ts` / `config-mock.ts` / `ws-plugin.ts`）。
 * **进程内只装 MSW 的话，后一套完全看不见** —— `/nodes` 和 `/rules` 都属于它，
 * 而那两个恰好是字段最多、最该被比的端点。
 *
 * 这曾经是一个写在下面 CASES 里的洞。它的代价是实的：`scope` / `key_type`
 * （前端声明了、后端从来没发过）和 `not_after`（后端一直在发、前端不知道）
 * 都是在别处偶然发现的 —— 它们本该由这里抓到。
 *
 * ## 两套 mock 的优先级跟浏览器里一致
 *
 * MSW 用 `bypass`：它拦得到的自己回，拦不到的**真的发到 vite server**，
 * 落到插件的 middleware 上。浏览器里也是这个顺序 —— service worker 先拦，
 * 没拦到才走网络。**分工一致，这个脚本面对的才是界面面对的那个世界。**
 *
 * ## `appType` 保持默认（spa）
 *
 * 不设成 `custom`：真 dev server 是 spa 模式，没有匹配的中间件时返回
 * index.html。一个不存在的端点在这里应该拿到那份 HTML，而不是 404 ——
 * 那正是 e2e 撞过的「JSON 第 5 个字符处意外」的来源，
 * 它是这个世界真实的样子。
 */
const vite = await createViteServer({
  // 避开 5173（dev）与常用的调试端口；strictPort 关着，占用了就往后找
  server: { port: 5199, strictPort: false, host: '127.0.0.1' },
  logLevel: 'error',
})
await vite.listen()
const viteAddr = vite.httpServer?.address()
if (!viteAddr || typeof viteAddr === 'string') {
  console.log('\n起不了本地 dev server，这个检查没法进行。\n')
  process.exit(1)
}
const MOCK_BASE = `http://127.0.0.1:${viteAddr.port}/api/v1`

/*
 * MSW 的 handler 里写的是相对路径（`/api/v1/...`），浏览器里靠 `location` 解析成
 * 绝对地址。Node 里没有 `location`，于是**一条都匹配不上** —— 而它的表现是
 * 「没有 handler 处理这个请求」，看起来像 mock 里少写了端点。给它一个 origin。
 *
 * **这个 origin 必须是 vite 那个 server 的，不能是随便一个 `http://localhost/`。**
 *
 * 改成打真 dev server 的时候我在这里绊了一次：origin 对不上，MSW 一条都没匹配，
 * 于是每个请求都 bypass 到 vite，落在插件那句「主控不会把 /api/v1/* 回落到
 * index.html」的 404 上。**11 个端点全红**，而报告看起来像 mock 整个坏了。
 *
 * 症状离原因很远 —— 一行 origin 的差别，表现成「所有端点都没有 mock」。
 */
globalThis.location = new URL(`http://127.0.0.1:${viteAddr.port}/`)

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
  /*
   * **`bypass` 而不是 `error`。** MSW 拦不到的请求要真的发出去 ——
   * 那是 Node 侧 vite 插件提供的那一半（`/nodes`、`/rules`…）。
   * 用 `error` 的话它们会在这里报「没有匹配的 handler」，
   * 而它们在浏览器里工作得好好的。
   *
   * 开关只在这个函数里 —— `real()` 也用 fetch，MSW 一直开着的话
   * 它会把发给真主控的请求也拦下来，于是这个脚本会拿 mock 跟 mock 比，
   * **而那永远是一致的**。
   */
  server.listen({ onUnhandledRequest: 'bypass' })
  try {
    const res = await fetch(MOCK_BASE + path, {
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
 * ── 第二道自检：**替身那一侧的两半都要真的在** ──
 *
 * 这个脚本现在打的是一个真 dev server，而替身有两半：MSW 的 handlers 和
 * Node 侧的 vite 插件。任何一半没接管，下面每一条分歧都不作数 ——
 * 而它们的表现**不是「装置坏了」，是「所有端点都对不上」**。
 *
 * 我改这个脚本时就踩了：`globalThis.location` 的 origin 还是旧的，
 * MSW 一条都没匹配、全部 bypass 到 vite，落在插件那句 404 上。
 * **11 个端点全红**，报告读起来像 mock 整个坏掉了 —— 症状离原因很远。
 *
 * 所以各探一个：`/auth/session` 只有 MSW 有，`/nodes` 只有插件有。
 */
const probeMsw = await mock('/auth/session')
const probeVite = await mock('/nodes')
if (probeMsw.status !== 200 || probeVite.status !== 200) {
  const dead = probeMsw.status !== 200 ? 'MSW' : 'vite 插件'
  console.log(`\n替身有一半没接管（${dead}），这个检查没法进行。`)
  console.log(`  /auth/session（MSW）HTTP ${probeMsw.status} · /nodes（插件）HTTP ${probeVite.status}`)
  if (probeMsw.status !== 200) {
    console.log('  MSW 的 handler 写的是相对路径，靠 `globalThis.location` 给 origin ——')
    console.log('  它要跟 dev server 的地址一致，否则一条都匹配不上而全部 bypass。')
  } else {
    console.log('  vite 插件由 `VITE_USE_MOCK !== "false"` 启用，检查一下环境变量。')
  }
  console.log('\n  下面每一条分歧都不作数，所以不往下报。\n')
  process.exit(1)
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
   * `/nodes` 与 `/rules` 由 **Node 侧的 vite 插件**提供，不是 MSW。
   *
   * 它们从前比不了 —— 这个脚本只在进程内装 MSW，那一半完全看不见。
   * 现在它打的是一个真的 dev server，两套 mock 都在后面（见上面 `MOCK_BASE`）。
   *
   * 这两个是字段最多的端点，代价也是实的：`scope` / `key_type`
   * （前端声明了、后端从来没发过，界面上一直是空格子）和 `not_after`
   * （后端一直在发、前端不知道）都是在别处偶然发现的 —— 它们本该由这里抓到。
   */
  {
    name: 'GET /nodes',
    path: '/nodes',
    /*
     * **这两个契约上就可空，而真主控此刻没有数据。**
     *
     * `dns_sync.targets`：没配 DNS 服务商就没同步过（契约 §11）。
     *   后端刚把 `omitempty` 去掉（`5dd443a`）—— 在那之前这个键**整个不存在**，
     *   而契约说的是「`null` 是旧数据，不是『一个目标都没有』」：
     *   **tag 让那句话没法成立**，三种写法只表达得出两个意思。
     *   现在键永远在，没数据时是 null。
     *
     * `cpu_series`：主控重启后那 12 个点要重新攒（契约 §4）。
     *
     * mock 两边都造了数据 —— 那是对的，界面要展示那个场景（同步失败按域名
     * 分开列、CPU 曲线）。真主控回 null 也是对的。**两边都对，而形状对不上。**
     *
     * 豁免不等于比过了：它们会出现在报告的 ⚠ 里，说「这一层一个字段都没比到」。
     *
     * ## 这个检查的结果依赖真主控此刻的状态
     *
     * `cpu_series` 这一条是刚重启主控时加的 —— 那时它是 null。**几分钟后
     * 再跑，它已经攒出 12 个点，这条豁免就用不上了。**
     *
     * 留着，因为下一个重启主控的人会再撞一次；而那时的红是假的。
     *
     * 但这件事本身要说出来：**同一份代码，对着一个刚起来的主控和一个跑了
     * 一天的主控，这个检查给出的结果不同。** 那是它的性质不是缺陷 ——
     * 它比的就是真环境，而真环境有状态。知道这一点，才不会把一次偶然的绿
     * 当成一次证明。
     */
    nullable: ['$.data.dns_sync.targets', '$.data.items[].cpu_series'],
  },
  { name: 'GET /rules', path: '/rules' },
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
  'DELETE /certs/:domain': '真删一张证书，而且会触发一次下发把它从各节点上摘掉',
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
  skipped = []
  nullables = new Set(c.nullable ?? [])
  const diffs = diffShape(shapeOf(m.body), shapeOf(r.body))
  rows.push({
    name: c.name,
    diffs,
    /** 因为空数组而没比到内容的路径 —— 见 diffShape 里那一段。 */
    empties: [...empties],
    /** 被 SKIP_PATHS 显式跳过的 —— 同样要说出来，少比一块不能是隐形的。 */
    skipped: [...skipped],
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

/*
 * **读端点也要交账。**
 *
 * 上面那笔账只对 request-shapes.json 里的**写端点**闭合——而读端点根本不在
 * 那份清单里，于是漏掉一个不会有任何地方问起（issue #72）。
 *
 * GET /dns/weights 是最吃亏的那个：in_rotation / weight_set / capabilities /
 * domains 都在它身上，字段最新最厚、mock 最容易分叉，而它既不在比对表里、
 * 也不需要写一句为什么。
 *
 * READ_ENDPOINTS 是这份账的**上界**（domain.md「登记表是上界」）：这里漏登记
 * 的端点这条检查看不见。它由 requests.ts 与 CASES 之外的人工维护，
 * 而那正是它需要被写下来的理由——写下来至少能被 review 看见。
 */
const READ_ENDPOINTS = [
  'GET /overview',
  'GET /nodes',
  'GET /routes',
  'GET /rules',
  'GET /drafts',
  'GET /deploys',
  'GET /deploys/:id',
  'GET /audit',
  'GET /certs',
  'GET /settings',
  'GET /alerts',
  'GET /dns/weights',
  'GET /policies/:id',
  'GET /nodes/:id/logs',
]
const comparedReads = new Set(
  CASES.filter((c) => !c.init?.method).map((c) => norm(`GET ${c.path}`)),
)
const NOT_COMPARED_READS = {
  'GET /nodes/:id/logs': '要先有一台真节点在上报日志，dev 环境里是空的',
}
const excusedReads = new Set(Object.keys(NOT_COMPARED_READS).map(norm))
const unaccountedReads = READ_ENDPOINTS.filter(
  (k) => !comparedReads.has(norm(k)) && !excusedReads.has(norm(k)),
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
  /*
   * **两个来源，措辞不能共用。**
   *
   * 一种是两边都空（种子有、真库没有），一种是一边 null（契约上可空、
   * 此刻没数据）。前一句套在后一种上是**一句假话** —— 而它正好出现在
   * 一个说「这里没比到」的位置上，读的人没有理由怀疑它。
   */
  const emptyNote = row.empties?.length
    ? row.empties
        .map((e) => `　⚠ ${e.path} ${e.why} —— 这一层的字段一个都没比到`)
        .join('')
    : ''
  /*
   * 跟上面那条同源：**少比一块不能是隐形的**。
   *
   * 空数组那种是碰上的，这种是我们自己决定跳过的 —— 后者更该说出来，
   * 因为「有意跳过」会随时间变成「忘了还有这么一块」。
   */
  const skipNote = row.skipped?.length
    ? row.skipped.map((k) => `　⚠ ${k.path} ${k.why}，没比（见 SKIP_PATHS）`).join('')
    : ''

  if (!hard.length && !statusDiff) {
    console.log(`✓ ${row.name} ${softNote}${emptyNote}${skipNote}`)
    continue
  }
  bad++
  console.log(`✗ ${row.name} ${softNote}${emptyNote}${skipNote}`)
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

if (unaccountedReads.length) {
  bad += unaccountedReads.length
  console.log('')
  console.log('✗ 这些读端点既没比、也没写明为什么不比：')
  for (const k of unaccountedReads) console.log(`    ${k}`)
  console.log('  加进 CASES，或者在 NOT_COMPARED_READS 里写一句理由。')
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
/*
 * **结论行要说出这次有多少处没比到。**
 *
 * 那些 ⚠ 本来只在逐条明细里。而一个人看这个检查，多半只看最后一行 ——
 * 于是「11 个端点全比过了」和「11 个端点里有 3 层压根没比到」
 * **在他的读法里是同一句话**。
 *
 * 这条的来处是一个更硬的观察：这个检查的结果依赖真主控此刻的状态
 * （见 `/nodes` 那个 case 的注释），所以**它的绿不可比较** ——
 * 而它看起来完全可比：同一条命令、同一个绿。
 *
 * 把没比到的数目提到结论行，是让两次绿变得可以区分的最省事的办法：
 * 「一致（3 处没比到）」和「一致」是两个不同的结论，
 * **而它们本来长得一模一样**。
 */
const blind = rows.reduce((n, r) => n + (r.empties?.length ?? 0) + (r.skipped?.length ?? 0), 0)
console.log(
  blind
    ? `${rows.length} 个端点，mock 与真主控的形状一致 —— 但有 ${blind} 处没比到（上面的 ⚠）。`
    : `${rows.length} 个端点，mock 与真主控的形状一致。`,
)
console.log('')
/*
 * 显式退出。MSW 的拦截器和 Vite 的 watcher 会把事件循环挂住 —— 失败分支有
 * `process.exit(1)` 所以一直没暴露，**第一次全绿的时候它就再也不返回了**。
 * 一个只在成功时挂死的检查步骤，会让整条流水线停在「看起来还在跑」。
 */
process.exit(0)
