#!/usr/bin/env node
/**
 * DNS 凭据配上之后要验的那一遍 —— 前端这一侧。
 *
 * 写成脚本而不是留一句「凭据到了我跑一遍」，是因为**那句话我在消息里说了很多轮，
 * 而它一直只存在于句子里**。一句发出去的话没有人能查；一个脚本可以被跑。
 * 这跟我们花了一整天在清的那类东西是同一件事，只是它发生在承诺上。
 *
 * 凭据没配时它**应该红**，而且红得说清「还没到能验的时候」—— 那不是失败，
 * 是这一遍还没轮到。所以它单独跑，不进 `pnpm check`。
 *
 *   node scripts/check-certs.mjs [--base …] [--user …] [--pass …]
 *   node scripts/check-certs.mjs --base <mock> --no-login   # 让断言主体先跑一遍
 *
 * `--no-login` 只给「拿 mock 数据把断言逻辑本身走一遍」用 —— **这个脚本写完那天，
 * 它的主体一条都没执行过**（真主控没配服务商，前置检查直接把它挡在门外）。
 * 一个从没跑过的检查，和一句「凭据到了我跑一遍」的承诺没有区别。
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
  const sc = res.headers.get('set-cookie')
  if (sc) cookie = sc.split(';')[0]
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
const check = async (what, where, fn) => {
  try {
    results.push({ ok: true, what, where, detail: (await fn()) ?? '' })
  } catch (e) {
    results.push({ ok: false, what, where, detail: e instanceof Error ? e.message : String(e) })
  }
}
const must = (c, m) => {
  if (!c) throw new Error(m)
}

/* ── 自检先跑：连不上或没登录时，下面每一条「没问题」都不作数 ── */
const login = await call('/auth/login', {
  method: 'POST',
  body: JSON.stringify({ username: USER, password: PASS }),
})
if (login.body?.code !== 0 && !process.argv.includes('--no-login')) {
  console.error(`\n✗ 登录失败：${login.body?.msg ?? login.status}\n  后面的断言不再执行。\n`)
  process.exit(2)
}

/* ── 前置：凭据配了没 ── */
const w = await call('/dns/weights')
const providerKind = w.body?.data?.capabilities?.kind ?? ''
if (!providerKind) {
  console.error(
    '\n⏸ 还没配 DNS 服务商 —— 这一遍还没轮到，不是失败。\n' +
      '  控制台「系统设置 → DNS 服务商」填上之后再跑。\n' +
      '  那一份凭据同时卡着三条链路：证书签发、DNS 真同步、下线的第一步。\n',
  )
  process.exit(3)
}
console.log(`\n服务商：${providerKind}\n`)

// ── 1. 证书的两列真相 ──
await check(
  '每张证书的「主控账面」与「节点回执」都能对上账',
  'src/views/CertsView.vue 的「N / M 个节点」',
  async () => {
    const r = await call('/certs')
    must(r.body?.code === 0, `取不到证书：${r.body?.msg}`)
    const items = r.body.data.items ?? []
    must(items.length > 0, '一张证书都没有 —— 先签发一张再跑这一遍')
    for (const c of items) {
      must(
        typeof c.expected_nodes === 'number' && typeof c.loaded_nodes === 'number',
        `${c.domain} 缺 expected_nodes / loaded_nodes，两列真相无从显示`,
      )
      must(
        c.loaded_nodes <= c.expected_nodes,
        `${c.domain} 回执(${c.loaded_nodes}) 多于账面(${c.expected_nodes}) —— 这个方向不该发生`,
      )
      must(
        Array.isArray(c.missing_nodes),
        `${c.domain} 的 missing_nodes 不是数组 —— 界面展开不出「差哪几台」`,
      )
      must(
        c.missing_nodes.length === c.expected_nodes - c.loaded_nodes,
        `${c.domain} 差额 ${c.expected_nodes - c.loaded_nodes} 台，而 missing_nodes 列了 ` +
          `${c.missing_nodes.length} 台 —— 两个数字对不上，界面上会同时显示两个版本的真相`,
      )
    }
    return items
      .map((c) => `${c.domain} ${c.loaded_nodes}/${c.expected_nodes} ${c.issuer}`)
      .join('；')
  },
)

// ── 2. 每张证书都说得出自己从哪来 ──
/*
 * **这一条换过判据，而不是改了个值。**
 *
 * 原来它守的是「ACME 那一步真走了」：issuer 不是内部 CA，且 challenge 是
 * `dns-01`。ADR-0015 之后主控不再签发证书 —— **那个被守的行为整个没有了**，
 * 于是把 `'dns-01'` 顺手改成 `'imported'` 会得到一条看着还在工作、实际上
 * 什么都不再保护的检查。
 *
 * 现在守的是契约 §9 那一条：**导入的证书不能记成 `dns-01`**，因为那会让人
 * 以为主控自己跑过一次校验。而库里确实还留着 ADR-0015 之前的 `dns-01`，
 * 所以它们不算失败 —— 但要在摘要里点出来，那是「还没被换掉的历史证书」。
 */
await check(
  '每张证书的来源都标记正确（imported / dns-01 历史）',
  'src/certs/challenge.ts 的映射、CertsView 的「来源」列',
  async () => {
    const r = await call('/certs')
    const items = r.body?.data?.items ?? []
    must(items.length > 0, '没有证书')

    const KNOWN = new Set(['imported', 'dns-01'])
    // 内部 CA 的客户端证书（回源 mTLS 那张）既不是导入也不是 ACME，不归这条管
    const managed = items.filter((c) => !/internal|self-signed/i.test(c.issuer ?? ''))
    must(managed.length > 0, '一张受管证书都没有 —— 这一条测的是空集')

    for (const c of managed) {
      must(c.issuer, `${c.domain} 没有 issuer`)
      must(
        c.challenge && KNOWN.has(c.challenge),
        `${c.domain} 的 challenge 是「${c.challenge}」—— 不在 ${[...KNOWN].join(' / ')} 里，` +
          `界面会原样把它显示出来，而没人知道那是什么`,
      )
    }

    const legacy = managed.filter((c) => c.challenge === 'dns-01')
    const imported = managed.filter((c) => c.challenge === 'imported')
    return (
      `${imported.length} 张外部导入` +
      (legacy.length
        ? `；${legacy.length} 张仍是 ADR-0015 之前主控签的（${legacy.map((c) => c.domain).join('、')}）` +
          ` —— 主控不会再续它们，到期前要从外部平台重新导入`
        : '')
    )
  },
)

/*
 * ── 3.（已删）手动续期受理 ──
 *
 * 原来这里验 `POST /certs/:domain/renew` 会不会受理。**那个端点没了**
 * （ADR-0015：主控不签发也不续期），所以这条检查的对象不存在了。
 *
 * 删掉而不是留着跳过：一条永远不会红的检查会让通过数虚高，而那个数字是
 * 判断这套装置有多严的唯一依据。
 */

// ── 4. dns_sync.ok 该转真了 ──
await check(
  'dns_sync.ok 为真，且 at 是真实时刻',
  'src/nodes/flags.ts 的徽标 / DnsView 的横幅',
  async () => {
    const r = await call('/nodes')
    const s = r.body?.data?.dns_sync
    must(s, '/nodes 顶层没有 dns_sync')
    must(s.ok === true, `dns_sync.ok 仍是 false：${s.detail}`)
    must(s.at && !String(s.at).startsWith('0001-01-01'), `at 不是真实时刻：${JSON.stringify(s.at)}`)
    return `ok=true，at=${s.at}`
  },
)

// ── 5. 下线三步，第一步要真摘解析 ──
await check(
  '下线的第一步真的动了服务商，不只是改标志位',
  'DrainConfirm 的结果面板（三步各自的 ok 与 detail）',
  async () => {
    const nodes = await call('/nodes')
    const target = (nodes.body?.data?.items ?? []).find((n) => !n.drained_at && n.dns_enabled)
    must(target, '找不到一台「没下线且解析开着」的节点来试')

    const d = await call(`/nodes/${target.id}/drain`, {
      method: 'POST',
      body: JSON.stringify({ confirm: true }),
    })
    must(d.body?.code === 0, `下线失败：${d.body?.msg}`)
    const steps = d.body.data.steps ?? []
    const dns = steps.find((s) => s.step === 'dns_removed')
    must(dns, '没有 dns_removed 这一步')
    must(dns.ok === true, `摘解析没成功：${dns.detail}`)
    const drained = steps.find((s) => s.step === 'conns_drained')
    must(drained, '没有 conns_drained 这一步')
    must(
      String(drained.detail ?? '').includes('缓存'),
      `排空那步的 detail 少了 TTL 边界那句：${drained.detail} —— ` +
        '不说的话人会据此关机，而解析缓存没过期前仍有新连接',
    )

    // 复原，别把一台机器留在下线状态
    await call(`/nodes/${target.id}/rejoin`, { method: 'POST' })
    await call(`/nodes/${target.id}/dns`, { method: 'POST', body: JSON.stringify({ enabled: true }) })
    return steps.map((s) => `${s.step}=${s.ok}`).join(' ')
  },
)

/* ── 报告 ── */
console.log('')
for (const r of results) {
  console.log(`${r.ok ? '✓' : '✗'} ${r.what}`)
  console.log(`    依赖处：${r.where}`)
  console.log(`    ${r.detail}`)
}
const bad = results.filter((r) => !r.ok)
console.log('')
if (bad.length) {
  console.error(`${bad.length} / ${results.length} 条没过。\n`)
  process.exit(1)
}
console.log(`${results.length} 条全过 —— 证书这一块终于验完了。\n`)
