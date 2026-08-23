#!/usr/bin/env node
/**
 * 打包控制台产物，**并且验它**。
 *
 * 在此之前这是我每次手敲的一串 bash。灰度上撞到的那个 bug 正是这么混进来的：
 *
 *   $ tar tzvf edge-console-*.tar.gz | head -1
 *   drwx------  ./          ← 顶层目录 0700
 *
 * 灰度现场 `namei -l` 出来的是这个：
 *
 *   drwxr-xr-x root root   /
 *   drwx------ 501  staff  opt        ← 就是它
 *   drwxr-xr-x edge edge   edge
 *   drwxr-xr-x edge edge   web
 *
 * **被印上的是 `/opt` 本身，模式 0700、属主 UID 501 / staff** —— 我这台 macOS
 * 的账号，印在一台 Debian 上。解包时没带 `-C`（`cd /opt && sudo tar xzf`），
 * 于是归档里那个 `./` 条目对应到了 `/opt`，而 GNU tar 以 root 跑会把它的
 * 模式**和属主**一起恢复上去。
 *
 * 所以问题不只是模式，**是这个归档带着我的身份出门**。把模式改成 755 只治了
 * 一半：`/opt` 照样会被 chown 成 501:staff，那是一个 Debian 上不存在的用户。
 *
 * ## 两处一起改
 *
 * 1. **顶层放一个具名目录 `edge-console/`**，不再是 `./`。归档里不再有任何
 *    条目对应到解包目标，它就再也无法改目标目录的模式或属主。
 *    （另一个选项是「不带顶层条目」，但那样忘了 `--strip-components=1` 时
 *    **什么都解不出来而且不报错**；具名目录会多出可见的一层，错得看得见。
 *    这一条是后端的判断，我同意：**把静默失败换成可见的错**。）
 * 2. **`--uid 0 --gid 0 --uname root --gname root`** —— 归档不携带打包机器的
 *    身份。501:staff 在目标机器上什么都不是。
 *
 * ## 一段我说错又更正的
 *
 * 我曾在这里写死「`--strip-components=1` 会把 `./` 的模式盖到目标目录上」，
 * 那是照着后端的诊断复述的，我没量过。本机量出来带不带 strip 都不改目标目录
 * （BSD tar、非 root），于是去更正 —— 而正是那次更正让后端没在
 * `/opt/edge/web` 上继续绕，`namei` 才指到了 `/opt`。
 *
 * **真因跟 `--strip-components` 一点关系都没有。**
 *
 * ## 成因不会消失
 *
 * `mktemp -d` 在 macOS 上默认就是 0700。**只要打包还在这台机器上做，
 * 它每次都会这样** —— 所以不是「这次记得 chmod」，是把 chmod 和校验一起
 * 焊进这个脚本，再也不手敲。
 *
 * ## 它验什么
 *
 * 1. **每个条目都在 `edge-console/` 下**，没有 `./`、没有绝对路径、没有 `..`。
 * 2. **属主是 root/root** —— 归档里不能有打包机器的身份。
 * 3. 每个条目 `go+rX`：目录 `o+x`、文件 `o+r`。
 * 4. 解包后与 `dist/` 逐字节一致 —— 打包过程没吞掉也没改写任何东西。
 * 5. 没有 mock（`check:dist` 也查，这里再查一遍：**这一份才是发出去的**）。
 * 6. 有 `版本.txt`，且里面的 commit 是当前 HEAD。
 *
 *   node scripts/pack.mjs
 */

import { execFileSync } from 'node:child_process'
import { existsSync, mkdtempSync, mkdirSync, cpSync, writeFileSync, chmodSync, readdirSync, statSync, rmSync, readFileSync } from 'node:fs'
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

const TOP = 'edge-console'
const stage = mkdtempSync(join(tmpdir(), 'ec-pack-'))
const root = join(stage, TOP)

/**
 * **先把 stage 自己 chmod 成 755。**
 *
 * 这一行就是那个 bug 的全部修复。`mkdtempSync` 跟 `mkdtemp(3)` 一样给 0700，
 * 而 tar 记的是目录当时的模式 —— 顶层 `./` 带着 0700 出门，
 * `--strip-components=1` 再把它盖到 `/opt/edge/web` 上。
 */
chmodSync(stage, 0o755)
mkdirSync(root, { recursive: true })

cpSync(join(DIST_SRC, 'assets'), join(root, 'assets'), { recursive: true })
cpSync(join(DIST_SRC, 'index.html'), join(root, 'index.html'))

writeFileSync(
  join(root, '版本.txt'),
  `edge-console
commit  ${sha}
built   ${built}

伺服方式：主控 EC_WEB_ROOT 指向本包解包后的目录。
  - 资源路径是根绝对路径，必须挂在域名根下
  - 深层路由要 SPA fallback（/nodes、/workbench/route:api.example.com 这类）
  - /assets/* 找不到要 404，不能 fallback —— 那个路径下只有一种消费者，
    而浏览器会把 HTML 当 JavaScript 执行

解包（顶层是 edge-console/，归档不碰目标目录的模式与属主）：
  tar xzf edge-console-*.tar.gz -C /opt/edge/web --strip-components=1

之后修属主，然后**用主控那个用户**验一遍：
  chown -R edge:edge /opt/edge/web
  sudo -u edge test -r /opt/edge/web/index.html && echo ok
**用 root 验等于没验** —— root 读得到不说明 edge 读得到。

读不到的时候，第一条命令是 namei -l /opt/edge/web/index.html ——
挡路的可能在任何一层。灰度上那次是 /opt 自己（0700，属主 UID 501），
而所有人都在盯着 /opt/edge/web。
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
normalize(root)

/* ── 打 ─────────────────────────────────────────────────────────────── */

mkdirSync(OUT_DIR, { recursive: true })
const tgz = join(OUT_DIR, `edge-console-${sha}.tar.gz`)
/*
 * `--uid 0 --gid 0 --uname root --gname root`：**归档不带打包机器的身份**。
 * 灰度上 `/opt` 的属主变成 UID 501 / staff，就是我的 macOS 账号跟着包过去的。
 *
 * 打的是 `edge-console`（一个具名目录）而不是 `.`，所以归档里不存在任何
 * 对应到解包目标的条目 —— 它再也改不了目标目录的模式或属主。
 */
sh('tar', [
  '--uid', '0', '--gid', '0', '--uname', 'root', '--gname', 'root',
  '-czf', tgz, '-C', stage, TOP,
])
rmSync(stage, { recursive: true, force: true })

/* ── 验 ─────────────────────────────────────────────────────────────── */

const problems = []

// 1~3. 路径、属主、模式
const listing = sh('tar', ['tzvf', tgz]).split('\n').filter(Boolean)
if (listing.length < 10) problems.push(`包里只有 ${listing.length} 个条目，太少了`)

/*
 * 不去数日期有几段。
 *
 * 第一版按 `模式 链接数 属主 组 大小 日期 名字` 写了个正则，而 `tar tzvf` 的
 * 日期在这台机器上是 `8月 23 11:07` —— **三段，还带中文**。于是「名字」从
 * `23` 开始，每一条都被判成「不在 edge-console/ 下」，49 条全红。
 *
 * 前四个字段是稳的，名字是最后一段，中间那些不看。**一个按位置数字段的解析器，
 * 数错了不会报错，它会给出一个看起来很确定的错答案。**
 */
let parsed = 0
for (const line of listing) {
  const parts = line.split(/\s+/)
  if (parts.length < 6 || !/^[drwx@+.-]{10,11}$/.test(parts[0])) continue
  parsed++
  const mode = parts[0]
  const owner = parts[2]
  const group = parts[3]
  const name = parts[parts.length - 1]

  // 路径：全都在 edge-console/ 下，不能有 ./、绝对路径、..
  if (!name.startsWith(`${TOP}/`) && name !== `${TOP}/` && name !== TOP) {
    problems.push(`${name} 不在 ${TOP}/ 下 —— 归档不该有对应解包目标的条目`)
  }
  if (name.startsWith('/') || name.split('/').includes('..')) {
    problems.push(`${name} 是绝对路径或含 ..`)
  }

  // 属主：不能带打包机器的身份
  if (owner !== 'root' || group !== 'root') {
    problems.push(`${name} 的属主是 ${owner}/${group} —— 灰度上 /opt 变成 501/staff 就是这么来的`)
  }

  // 模式：位 7/8/9 是 other 的 r/w/x
  if (mode[7] !== 'r') problems.push(`${name} 其他人读不到（${mode}）`)
  if (mode[0] === 'd' && mode[9] !== 'x') {
    problems.push(`${name} 是目录但其他人进不去（${mode}）`)
  }
}

/*
 * **一条都没解析出来时要红。**
 *
 * 上面每一条断言都是「不该出现 X」，而否定断言在装置失效时会一起变绿 ——
 * 正则跟 `tar tzvf` 的输出格式对不上的话，循环一次都不进，
 * 而输出跟「全都合格」一模一样。
 */
if (parsed !== listing.length) {
  problems.push(
    `tar 输出有 ${listing.length} 行，只解析出 ${parsed} 行 —— 没解析的那些一条都没被检查`,
  )
}

// 2. 解包后与 dist 一致
const check = mkdtempSync(join(tmpdir(), 'ec-verify-'))
chmodSync(check, 0o755)
sh('tar', ['xzf', tgz, '-C', check])
const unpacked = join(check, TOP)

/*
 * **解不出 `edge-console/` 就到此为止，别往下走。**
 *
 * 探针把打包改回 `-C root .` 时，上面那几条已经算出「不在 edge-console/ 下」
 * 了，而脚本在这里崩在 `scandir ENOENT` 上 —— **报告在最后，崩了就一个字都
 * 说不出来**。人看到的是一句 `ENOENT`，而真正的诊断已经躺在 problems 里。
 *
 * 一个自己会崩的检查器，比一个报错不清的检查器更糟：后者至少在说话。
 */
if (!existsSync(unpacked)) {
  problems.push(`包里没有 ${TOP}/ 这一层，后面几条没法验 —— 先看上面那些路径问题`)
}

try {
  if (!existsSync(unpacked)) throw new Error('skip')
  sh('diff', ['-r', join(DIST_SRC, 'assets'), join(unpacked, 'assets')])
  sh('diff', [join(DIST_SRC, 'index.html'), join(unpacked, 'index.html')])
} catch (e) {
  if (!(e instanceof Error && e.message === 'skip')) {
    problems.push('解包后与 web/dist 不一致 —— 打包过程改动了产物')
  }
}

// 3. 没有 mock
const all = []
;(function walk(d) {
  if (!existsSync(d)) return
  for (const n of readdirSync(d)) {
    const p = join(d, n)
    if (statSync(p).isDirectory()) walk(p)
    else all.push(p)
  }
})(unpacked)
for (const f of all) {
  if (/mockServiceWorker/i.test(f)) problems.push(`包里混进了 ${relative(unpacked, f)}`)
}

// 4. 版本戳指向当前 HEAD
if (existsSync(join(unpacked, '版本.txt'))) {
  const ver = readFileSync(join(unpacked, '版本.txt'), 'utf8')
  if (!ver.includes(`commit  ${sha}`)) problems.push(`版本.txt 里的 commit 不是 ${sha}`)
} else {
  problems.push('包里没有 版本.txt')
}
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
console.log(`    顶层 ${TOP}/ · 属主 root/root · 目录 755 / 文件 644`)
console.log(`    归档对解包目标目录没有任何意见（模式和属主都不碰）`)
console.log(`    解包后与 web/dist 逐字节一致，没有 mock`)
console.log('')
process.exit(0)
