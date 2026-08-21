#!/usr/bin/env node
/**
 * 把「我以为后端会怎样」逐条拿去问真主控。
 *
 * 为什么单独一个脚本，而不是塞进 vitest：**这些断言的对象不是我的代码，是世界。**
 * 单测里的 fetch 是我自己造的，e2e 里的后端是我自己写的 mock —— 两者都能证明
 * 我的代码兑现了我的假设，都证明不了假设本身成立。而假设失效时，代码是绿的。
 *
 * 每一条都对应一处「因为后端会 X，所以前端这样写」。前提没了，那处代码就从
 * 「正确」变成「碰巧」，而没有任何东西会红。
 *
 *   node scripts/check-premises.mjs [--base http://localhost:8080/api/v1] \
 *                                   [--user fe] [--pass fe-dev-pass]
 */

const arg = (k, d) => {
  const i = process.argv.indexOf(`--${k}`)
  return i > 0 ? process.argv[i + 1] : d
}
const BASE = arg('base', 'http://localhost:8080/api/v1')
const USER = arg('user', 'fe')
const PASS = arg('pass', 'fe-dev-pass')

let cookie = ''
async function call(path, init = {}) {
  const res = await fetch(BASE + path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(cookie ? { cookie } : {}), ...init.headers },
  })
  const setCookie = res.headers.get('set-cookie')
  if (setCookie) cookie = setCookie.split(';')[0]
  const text = await res.text()
  let body = null
  try {
    body = JSON.parse(text)
  } catch {
    /* 非 JSON 也要能被断言看见 */
  }
  return { status: res.status, ok: res.ok, body, text }
}

const results = []
const check = async (premise, where, fn) => {
  try {
    const detail = await fn()
    results.push({ ok: true, premise, where, detail: detail ?? '' })
  } catch (e) {
    results.push({ ok: false, premise, where, detail: e instanceof Error ? e.message : String(e) })
  }
}
const must = (cond, msg) => {
  if (!cond) throw new Error(msg)
}

/*
 * 自检先跑。
 *
 * 一个连不上主控、或者登录没成功的脚本，会让下面每一条「不该出现 X」的断言
 * 因为**什么都没匹配到**而全绿 —— 一个什么也没检查的检查器，而且它是绿的。
 * 这比红的更难被发现（后端在 caddy list-modules 那条上撞到过同一个）。
 */
await check('自检：主控可达且登录成功', 'scripts/check-premises.mjs', async () => {
  const login = await call('/auth/login', {
    method: 'POST',
    body: JSON.stringify({ username: USER, password: PASS }),
  })
  must(login.body?.code === 0, `登录失败：${login.body?.msg ?? login.status}`)
  const s = await call('/auth/session')
  must(s.body?.data?.username === USER, '会话没建立，后面的断言全部不作数')
  const nodes = await call('/nodes')
  must(Array.isArray(nodes.body?.data?.items), '/nodes 没回 items，装置不对')
  return `以 ${USER} 登录，/nodes 回了 ${nodes.body.data.items.length} 条`
})

if (!results[0].ok) {
  console.error(`\n✗ 自检没过：${results[0].detail}\n  后面的断言不再执行 —— 它们会因为什么都没查到而全绿。\n`)
  process.exit(2)
}

// ── 契约 §0.2：HTTP 状态码与 code 不重复表达同一件事 ──
await check(
  '404 的包裹体里 code 仍然是 0',
  'src/api/http.ts 的 `if (!res.ok) throw`',
  async () => {
    const r = await call('/routes/definitely-not-a-real-domain.invalid')
    must(!r.ok, `期望 HTTP 4xx，实际 ${r.status}`)
    must(
      r.body?.code === 0,
      `code 是 ${r.body?.code} 而不是 0 —— 前提变了：现在只判 code 也能发现这个错误，` +
        `那条 !res.ok 抛出的理由该重写（但**别删**，两者都判才是对的）`,
    )
    return `HTTP ${r.status}，code ${r.body.code}，msg「${r.body.msg}」`
  },
)

// ── 契约 §0.3：1002 的 field 是点号路径，前端据此索引表单字段 ──
await check(
  '1002 带 errors[].field 与 reason，且 field 指得到请求体里真实存在的路径',
  'src/api/http.ts 的 errorText / FieldList 的 serverErrors',
  async () => {
    /*
     * 用一个**永远不会被创建成功**的 id：这条请求故意不带密钥，所以校验必拒，
     * 库里不会留下东西。和下面那条写密钥的检查各用各的 id —— 共用一个的话，
     * 上一轮写进去的密钥会让这一轮的「不给密钥」被接受（留空即不改），
     * 于是这条检查在第二次运行时就悄悄失去意义。
     *
     * 这本来该由清理来保证，但**主控没有 DELETE /rules/:id**（已报后端）。
     * 一个删不掉的夹具，只能靠不产生它来避开。
     */
    const r = await call('/rules/__premise_never_created__', {
      method: 'PUT',
      body: JSON.stringify({
        name: '前提核查（预期被拒，不会入库）',
        type: 'service_secret',
        enabled: true,
        apply_to: ['api.example.com'],
        spec: { header: 'X-K', algo: 'hmac-sha256', ttl_s: 300 },
      }),
    })
    must(r.body?.code === 1002, `期望 code 1002，实际 ${r.body?.code}`)
    const errs = r.body?.data?.errors
    must(Array.isArray(errs) && errs.length > 0, '1002 没带 errors —— errorText 会退回笼统的 msg')
    must(errs[0].reason, 'errors[0] 没有 reason —— 一条说不清原因的错误等于没有这条错误')
    must(
      !errs.some((e) => e.field?.startsWith('spec.secret')),
      'field 又指回 spec.secret 了 —— 密钥在请求体顶层，指向不存在的路径会让这条错误掉在地上',
    )
    return errs.map((e) => `${e.field} → ${e.reason}`).join('；')
  },
)

// ── PRD §7：凭证只写入不回显 ──
await check(
  '共享密钥写进去之后，任何读接口都取不回明文',
  'src/stores/config.ts 的 setRuleSecret / AclView 的「更换密钥」',
  async () => {
    const SECRET = 'premise-check-secret-do-not-echo'
    const put = await call('/rules/__premise_secret__', {
      method: 'PUT',
      body: JSON.stringify({
        name: '前提核查',
        type: 'service_secret',
        enabled: true,
        apply_to: ['api.example.com'],
        spec: { header: 'X-K', algo: 'hmac-sha256', ttl_s: 300 },
        secret: SECRET,
      }),
    })
    must(put.body?.code === 0, `写入失败：${put.body?.msg}`)
    const list = await call('/rules')
    must(!list.text.includes(SECRET), '**密钥被 GET /rules 回显了**')
    const drafts = await call('/drafts')
    must(!drafts.text.includes(SECRET), '**密钥出现在草稿里** —— 草稿是全局可见的')
    const mine = list.body.data.items.find((x) => x.id === '__premise_secret__')
    must(mine?.spec?.secret_configured === true, 'spec.secret_configured 不是 true，界面无从显示已配置')
    /*
     * **不清理，因为清理不了**：主控没有 DELETE /rules/:id（已报后端）。
     * 早先这里写了一句 DELETE，它 404 了而我没看返回值 —— 一个没生效的清理，
     * 表现成了清理过了，正是这条脚本要抓的那种东西，只不过发生在脚本自己身上。
     */
    return '写入后 /rules 与 /drafts 均无明文，secret_configured 为 true（夹具 __premise_secret__ 会留在库里，主控无法删除规则）'
  },
)

// ── 契约 §4：Token 只在签发那一次出现 ──
await check(
  '接入 Token 签发后不再从任何接口回显',
  'AddNodeModal 那句「关闭后 Token 无法再取回」',
  async () => {
    const r = await call('/nodes/token', {
      method: 'POST',
      body: JSON.stringify({
        node_id: '__premise_check__',
        city: 'x',
        vendor: 'y',
        line: 'z',
        public_ip: '203.0.113.99',
      }),
    })
    must(r.body?.code === 0, `签发失败：${r.body?.msg}`)
    const token = r.body.data.token
    must(token, '响应里没有 token')
    const nodes = await call('/nodes')
    must(!nodes.text.includes(token), '**Token 被 /nodes 回显了** —— 那句「只显示这一次」就是假的')
    return `Token 只出现在签发响应里；ca_pin 与 verify_cmd ${r.body.data.ca_pin && r.body.data.verify_cmd ? '都在' : '缺失'}`
  },
)

// ── 契约 §0.4：没有这个值就是 null，不是零值 ──
await check(
  'dns_sync.at 要么是 null，要么是一个真实时刻 —— 永不为零值时间',
  'src/utils/format.ts 的 fmtClock / model.ts 的 isZeroTime',
  async () => {
    const r = await call('/nodes')
    const sync = r.body?.data?.dns_sync
    must(sync, '/nodes 顶层没有 dns_sync —— 徽标会退回从 capabilities 推断')
    /*
     * 第一版这里写的是「ok 为 false 时 at 必须是 null」，**那是我写错了**：
     * ok=false 覆盖两种情况 —— 从没试过、和试过但失败了。后者的 at 是真实时刻，
     * 而且正是要显示给人看的那个「上次是什么时候没成功的」。
     *
     * 我要钉的从来不是 null 本身，是**不能出现一个格式正确而意思是假的值**。
     * 零值时间会被渲染成一个像模像样的 00:00:00，读起来像「凌晨同步过一次」，
     * 而空白会让人去查、一个像样的时间不会。
     */
    if (sync.at === null) return `at 为 null（从没同步过），detail「${sync.detail}」`
    must(
      !String(sync.at).startsWith('0001-01-01'),
      `at 是 Go 的零值时间 ${sync.at} —— 它会被渲染成一个像模像样的 00:00:00`,
    )
    must(
      !Number.isNaN(new Date(sync.at).getTime()),
      `at 解析不出时间：${JSON.stringify(sync.at)}`,
    )
    return `ok=${sync.ok}，at=${sync.at}（真实时刻），detail「${sync.detail}」`
  },
)

// ── 契约 §6.3：全局策略的默认值由渲染器给，且限流默认关着 ──
await check(
  'GET /policies/log 回补齐后的默认值，且 rate_limit 是 false',
  'src/workbench/fields.ts 的限流置灰；FieldList 的「未设置」态',
  async () => {
    const r = await call('/policies/log')
    must(r.body?.code === 0, `取不到 log 策略：${r.body?.msg}`)
    const spec = r.body.data.spec
    /*
     * 空 spec 也是一种「格式正确而意思是假的」：界面会把每个枚举画成没选中，
     * 而人无从判断此刻什么在生效。所以先断言它**不是空的**。
     */
    must(Object.keys(spec).length > 0, 'spec 是空的 —— 界面会把「不知道」渲染成「还没设置」')
    must(spec.format && spec.level, `缺默认值：${JSON.stringify(spec)}`)
    /*
     * 这条原先是前端单测里的「默认不开限流」，而它测的是我自己夹具里写的
     * false。后端把默认改回 true 的话，那条照样绿 —— 它是一条关于世界的声明，
     * 归这里。true 是个下发一定会被拒的值（官方 Caddy 没有限流模块）。
     */
    must(
      spec.rate_limit === false,
      `rate_limit 默认是 ${spec.rate_limit} —— true 会让下发被拒，等于文档记的默认值推不动`,
    )
    return `format=${spec.format} level=${spec.level} rate_limit=${spec.rate_limit}`
  },
)

// ── 契约 §6.4：草稿是 Partial，原样存原样回 ──
await check(
  '草稿 PUT 进去什么样，GET 回来就什么样',
  'src/workbench/draft.ts 的 merge / prune',
  async () => {
    const KEY = 'route:__premise_check__.invalid'
    const patch = { upstream: '10.8.0.99:8080', whitelist: ['203.0.113.0/24'] }
    const put = await call(`/drafts/${encodeURIComponent(KEY)}`, {
      method: 'PUT',
      body: JSON.stringify(patch),
    })
    must(put.body?.code === 0, `写草稿失败：${put.body?.msg}`)
    const got = await call('/drafts')
    const back = got.body?.data?.items?.[KEY]
    must(back, '草稿没回来')
    must(
      JSON.stringify(back) === JSON.stringify(patch),
      `回来的不是原样：${JSON.stringify(back)}`,
    )
    await call(`/drafts/${encodeURIComponent(KEY)}`, {
      method: 'PUT',
      body: JSON.stringify({}),
    })
    return 'Partial 原样往返，空对象删除该条'
  },
)

/* ── 报告 ── */
const bad = results.filter((r) => !r.ok)
console.log('')
for (const r of results) {
  console.log(`${r.ok ? '✓' : '✗'} ${r.premise}`)
  console.log(`    依赖处：${r.where}`)
  console.log(`    ${r.detail}`)
}
console.log('')
if (bad.length) {
  console.error(
    `${bad.length} / ${results.length} 条前提不成立。\n` +
      `**这不一定意味着前端有 bug** —— 它意味着上面那几处代码的理由已经失效，\n` +
      `该做的是重判那个理由，而不是把断言改绿。\n`,
  )
  process.exit(1)
}
console.log(`${results.length} 条前提全部成立。\n`)
