#!/usr/bin/env node
/**
 * 打包控制台产物，**并且验它**。
 *
 * 在此之前这是我每次手敲的一串 bash。灰度上撞到的那个 bug 正是这么混进来的：
 *
 *   $ tar tzvf edge-console-*.tar.gz | head -1
 *   drwx------  ./          ← 顶层目录 0700
 *
 * 部署文档里是 `tar xzf ... --strip-components=1`，而 **`--strip-components=1`
 * 会把 `./` 的模式盖到目标目录本身上**。解出来是 `drwx------ root root`，
 * 主控跑在 `User=edge` 下，连目录都进不去。
 *
 * 现场最刺眼的一幕：root 跑 `ls` 看到文件都在，而主控页面说「静态文件不在」。
 * **两句话直接矛盾，因为看的人是 root，主控不是。**
 *
 * ## 成因不会消失
 *
 * `mktemp -d` 在 macOS 上默认就是 0700。**只要打包还在这台机器上做，
 * 它每次都会这样** —— 所以不是「这次记得 chmod」，是把 chmod 和校验一起
 * 焊进这个脚本，再也不手敲。
 *
 * ## 它验什么
 *
 * 1. 每个条目 `go+rX`：目录 `o+x`、文件 `o+r`。**顶层 `./` 尤其**。
 * 2. 解包后与 `dist/` 逐字节一致 —— 打包过程没吞掉也没改写任何东西。
 * 3. 没有 mock（`check:dist` 也查，这里再查一遍：**这一份才是发出去的**）。
 * 4. 有 `版本.txt`，且里面的 commit 是当前 HEAD。
 *
 *   node scripts/pack.mjs
 */

import { execFileSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, cpSync, writeFileSync, chmodSync, readdirSync, statSync, rmSync, readFileSync } from 'node:fs'
import { join, dirname, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'

const WEB = join(dirname(fileURLToPath(import.meta.url)), '..')
const REPO = join(WEB, '..')
const DIST_SRC = join(WEB, 'dist')
const OUT_DIR = join(REPO, 'dist')

const sh = (cmd, args, cwd) =>
  execFileSync(cmd, args, { cwd: cwd ?? REPO, encoding: 'utf8' }).trim()

const sha = sh('git', ['rev-parse', '--short', 'HEAD'])
const built = new Date().toISOString().replace(/\.\d+Z$/, 'Z')

/* ── 摆产物 ─────────────────────────────────────────────────────────── */

const stage = mkdtempSync(join(tmpdir(), 'ec-pack-'))

/**
 * **先把 stage 自己 chmod 成 755。**
 *
 * 这一行就是那个 bug 的全部修复。`mkdtempSync` 跟 `mkdtemp(3)` 一样给 0700，
 * 而 tar 记的是目录当时的模式 —— 顶层 `./` 带着 0700 出门，
 * `--strip-components=1` 再把它盖到 `/opt/edge/web` 上。
 */
chmodSync(stage, 0o755)

cpSync(join(DIST_SRC, 'assets'), join(stage, 'assets'), { recursive: true })
cpSync(join(DIST_SRC, 'index.html'), join(stage, 'index.html'))

writeFileSync(
  join(stage, '版本.txt'),
  `edge-console
commit  ${sha}
built   ${built}

伺服方式：主控 EC_WEB_ROOT 指向本包解包后的目录。
  - 资源路径是根绝对路径，必须挂在域名根下
  - 深层路由要 SPA fallback（/nodes、/workbench/route:api.example.com 这类）
  - /assets/* 找不到要 404，不能 fallback —— 那个路径下只有一种消费者，
    而浏览器会把 HTML 当 JavaScript 执行

解包之后**必须**修属主与权限，然后用主控那个用户验一遍：
  chown -R edge:edge /opt/edge/web
  chmod -R u=rwX,go=rX /opt/edge/web
  sudo -u edge test -r /opt/edge/web/index.html && echo ok
**用 root 验等于没验** —— root 读得到不说明 edge 读得到。
`,
  'utf8',
)

/** 目录 755、文件 644。不依赖归档工具的 `--mode`：BSD tar 与 GNU tar 不一样。 */
function normalize(dir) {
  chmodSync(dir, 0o755)
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) normalize(p)
    else chmodSync(p, 0o644)
  }
}
normalize(stage)

/* ── 打 ─────────────────────────────────────────────────────────────── */

mkdirSync(OUT_DIR, { recursive: true })
const tgz = join(OUT_DIR, `edge-console-${sha}.tar.gz`)
sh('tar', ['czf', tgz, '-C', stage, '.'])
rmSync(stage, { recursive: true, force: true })

/* ── 验 ─────────────────────────────────────────────────────────────── */

const problems = []

// 1. 模式
const listing = sh('tar', ['tzvf', tgz]).split('\n')
if (listing.length < 10) problems.push(`包里只有 ${listing.length} 个条目，太少了`)
for (const line of listing) {
  const m = /^([drwx@+-]{10,11})\s/.exec(line)
  if (!m) continue
  const mode = m[1]
  const isDir = mode[0] === 'd'
  const name = line.slice(line.indexOf('./'))
  // 位 7/8/9 = other 的 r/w/x
  const oR = mode[7] === 'r'
  const oX = mode[9] === 'x'
  if (!oR) problems.push(`${name} 其他人读不到（${mode}）`)
  if (isDir && !oX) problems.push(`${name} 是目录但其他人进不去（${mode}）—— 就是这一条把灰度坑了`)
}

// 2. 解包后与 dist 一致
const check = mkdtempSync(join(tmpdir(), 'ec-verify-'))
chmodSync(check, 0o755)
sh('tar', ['xzf', tgz, '-C', check])
try {
  sh('diff', ['-r', join(DIST_SRC, 'assets'), join(check, 'assets')])
  sh('diff', [join(DIST_SRC, 'index.html'), join(check, 'index.html')])
} catch {
  problems.push('解包后与 web/dist 不一致 —— 打包过程改动了产物')
}

// 3. 没有 mock
const all = []
;(function walk(d) {
  for (const n of readdirSync(d)) {
    const p = join(d, n)
    if (statSync(p).isDirectory()) walk(p)
    else all.push(p)
  }
})(check)
for (const f of all) {
  if (/mockServiceWorker/i.test(f)) problems.push(`包里混进了 ${relative(check, f)}`)
}

// 4. 版本戳指向当前 HEAD
const ver = readFileSync(join(check, '版本.txt'), 'utf8')
if (!ver.includes(`commit  ${sha}`)) problems.push(`版本.txt 里的 commit 不是 ${sha}`)
rmSync(check, { recursive: true, force: true })

/* ── 校验和 ─────────────────────────────────────────────────────────── */

if (problems.length === 0) {
  const sum = sh('shasum', ['-a', '256', `edge-console-${sha}.tar.gz`], OUT_DIR)
  writeFileSync(join(OUT_DIR, 'SHA256SUMS.web'), sum + '\n', 'utf8')
}

/* ── 报告 ───────────────────────────────────────────────────────────── */

console.log('')
if (problems.length) {
  for (const p of problems) console.log(`✗ ${p}`)
  console.log('')
  console.log('包已生成但**没写校验和** —— 不要发它。')
  console.log('')
  process.exit(1)
}
console.log(`✓ ${relative(REPO, tgz)}`)
console.log(`    ${all.length} 个文件 · commit ${sha}`)
console.log(`    模式：目录 755 / 文件 644，顶层 ./ 也是（那一条就是灰度撞到的）`)
console.log(`    解包后与 web/dist 逐字节一致，没有 mock`)
console.log('')
process.exit(0)
