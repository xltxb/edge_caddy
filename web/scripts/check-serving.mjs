#!/usr/bin/env node
/**
 * dev server 与真主控在**静态伺服**上的行为是不是一回事。
 *
 * e2e 跑的是 dev server，生产是主控伺服静态文件（`internal/api/web.go`）。
 * 这两者在 SPA fallback、`/assets/` 404、`/api/` 不回落上各有各的实现 ——
 * **两边都对着自己的想象做**，而没有任何东西比过它们。
 *
 * 量过一次，三条不一致，方向都是 dev 更宽松：
 *
 *   /assets/ 下不存在的 chunk   主控 404 · dev 回落 index（200 HTML）
 *   /api/ 下不存在的端点        主控 404 · dev 回落 index（200 HTML）
 *   /ws                        主控 404 · dev 回落 index（200 HTML）
 *
 * **`/api/` 那条不是无害的**：它把「这个端点不存在」变成「返回了一段 HTML」，
 * 同一个 bug 在 dev 和生产上给出两种完全不同的症状。`GET /nodes/:id/logs`
 * 那次正是这样 —— 真主控 404，而 dev 下拿到的是一份 index.html。
 *
 * 规则本身是后端的（他们有 `web_test.go` 守着），这里比的是**我这边跟不跟得上**。
 * 所以断言不写死期望值，写「两边一样」：主控哪天改了规则，这里会红，
 * 而那正是我该知道的时刻。
 *
 *   node scripts/check-serving.mjs [--master http://localhost:8080]
 *                                  [--dev http://localhost:5173]
 *
 * 退出码：0 一致 · 1 有分歧 · 3 有一边没起（还没轮到，不是通过）
 */

const arg = (k, d) => {
  const i = process.argv.indexOf(`--${k}`)
  return i > 0 ? process.argv[i + 1] : d
}
const MASTER = arg('master', 'http://localhost:8080')
const DEV = arg('dev', 'http://localhost:5173')

async function probe(base, path) {
  const res = await fetch(base + path, { redirect: 'manual' })
  const head = (await res.text()).slice(0, 200).toLowerCase()
  const html = head.includes('<!doctype') || head.includes('<html')
  return { status: res.status, kind: html ? 'HTML' : '非HTML' }
}

/*
 * 每一条都写清**它为什么在这张表上**。
 *
 * 一张只有路径的表，下一个人删掉其中一行不会有任何阻力 —— 而这几条各自对应
 * 一个真实的失败：回落到 index 的 chunk 会让浏览器报「Unexpected token '<'」，
 * 而那句话完全指不到真因。
 */
const CASES = [
  { path: '/nodes', why: 'SPA 深层路由要回落到 index，否则刷新就 404' },
  { path: '/workbench/route:api.example.com', why: '路径里带冒号的深层路由' },
  {
    path: '/assets/does-not-exist-abc123.js',
    why: 'hash 变了的 chunk 必须 404。回落会返回 HTML，浏览器报「Unexpected token \'<\'」',
  },
  { path: '/api/v1/__no_such_endpoint__', why: '端点不存在要 404，不能变成「返回了一段 HTML」' },
  { path: '/ws', why: '实时通道的路径不回落' },
  { path: '/totally-not-a-route', why: '普通未知路径仍然回落 index（SPA 路由）' },
]

/* ── 自检：一边没起就退 3，不要把「两边都连不上」读成「两边一致」 ────── */

for (const [name, base] of [
  ['真主控', MASTER],
  ['dev server', DEV],
]) {
  try {
    await fetch(base + '/', { redirect: 'manual' })
  } catch (e) {
    console.log(`\n${name}（${base}）连不上，这个检查还没轮到。`)
    console.log(`  ${e instanceof Error ? e.message : String(e)}`)
    console.log('\n  它比的是两边行为一不一致 —— 少一边就无从比较。跳过不等于通过。\n')
    process.exit(3)
  }
}

const rows = []
for (const c of CASES) {
  const m = await probe(MASTER, c.path)
  const d = await probe(DEV, c.path)
  rows.push({ ...c, m, d, same: m.status === d.status && m.kind === d.kind })
}

console.log('')
for (const r of rows) {
  const mark = r.same ? '✓' : '✗'
  console.log(
    `${mark} ${r.path}\n` +
      `    主控 ${r.m.status} ${r.m.kind}   ·   dev ${r.d.status} ${r.d.kind}` +
      (r.same ? '' : `\n    ${r.why}`),
  )
}

const bad = rows.filter((r) => !r.same).length
console.log('')
if (bad) {
  console.log(`${bad} / ${rows.length} 条对不上。`)
  console.log('')
  console.log('规则由主控定（后端的 web_test.go 守着），这里比的是我这边跟不跟得上。')
  console.log('dev 的行为在 mocks/ws-plugin.ts 最后那道中间件里。')
  console.log('')
  process.exit(1)
}
console.log(`${rows.length} 条，dev server 与真主控的伺服行为一致。`)
console.log('')
process.exit(0)
