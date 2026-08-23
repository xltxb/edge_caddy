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
 * 6. 有 `版本.txt`，且里面的版本戳与这次打包算出来的一致。
 *
 * **第 6 条守不住「产物真的对应那个 commit」** —— 它比的是同一个变量，
 * 验的是「我写进去的等于我算出来的」。那件事由版本戳自己带 `-dirty` 来说，
 * 见下面 SCOPE / dirty 那两段：这个包一度戳着一个干净的 commit，
 * 而里面装着一整套那个 commit 上根本不存在的功能。
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

/**
 * **决定 dist 内容的那些路径。** 版本戳的作用域必须和产物的作用域一致。
 *
 * 不在这张表里的东西改了，产物一个字节都不会变，标成 dirty 就是在说一个
 * 不存在的差异 —— 后端撞过反过来的那一面：它的二进制里根本没有 `web/`，
 * 而我这边三个未提交的文件让它的产物叫上了 `-dirty`，**一个说「含未提交改动」
 * 而实际不含的版本戳，会让人去找一个找不到的差异。**
 *
 * ## 为什么是「整个 web/ 减去几样」，而不是列出该算的那几样
 *
 * 这里原先是白名单（`index.html` / `src` / `public` / 几个配置文件）。它当时
 * 是对的，而**它的失败方向是错的**：白名单漏掉一个真正影响构建的输入，
 * 产物变了而戳不变 —— 说干净而实际脏，那是贵的那一半。黑名单漏掉一个，
 * 只是多标一次 dirty —— 噪音。
 *
 * 而白名单**必然**会漏：它要求每加一个构建输入就有人记得回来改这张表。
 * 已经差点漏掉一个：`web/.env.real` 被 git 跟踪、不在白名单里。它眼下确实
 * 不影响生产构建（`vite build` 走 production，不读 `.env.real`），
 * 但**下一个 `.env.production` 就会影响，而没有任何东西会提醒谁来改这里**。
 *
 * 所以反过来：默认算数，排除项只留那些**确知不进产物**的，每一条都验过。
 *
 * `*.test.ts` 排掉：它们在 src 底下，但不进 import 图，改了不影响 dist。
 * `mocks/` `tests/` `scripts/` 同理 —— mock 是 dev-only 插件路径，
 * 生产构建里没有它（`check:dist` 每次都验这一条）。
 *
 * **排除项要两条，不是一条。** `web/src/**\/*.test.ts` 里的 `**` 要求至少一层
 * 中间目录，`web/src/model.test.ts` 这种直接躺在 src 下的匹配不上 —— 只写那
 * 一条的话，改一个 model.test.ts 就会让包叫上 `-dirty`，而产物一个字节没变。
 *
 * 这是写完之后跑一遍 `git status --porcelain -- <SCOPE>` 才发现的：
 * `nodes/flags.test.ts` 被排掉了、`model.test.ts` 还在列表里。
 * **一条只在某些输入上生效的排除规则，看起来和完全生效一模一样。**
 *
 * ## 改这张表之前，拿真实输入跑一遍
 *
 * 光看规则看不出它作用在谁身上 —— 上面那个 `**` 的洞，抽样查 `nodes/` 和
 * `palette/` 下的两个 test 文件都显示"排掉了"，规则看起来完全生效。
 *
 * 探法：往一个位置 `echo` 一个新文件，看它进不进
 * `git status --porcelain -- <SCOPE>` 的输出，然后删掉。
 * **只碰自己新建的文件**，别拿别人正在编辑的东西当探针素材。
 *
 * 量出来的结果（✓ = 与预期一致）：
 *
 *   web/src/xx.ts              触发    ✓  产物真的会变
 *   web/public/xx.txt          触发    ✓  会被原样拷进产物
 *   web/.env.production        触发    ✓  ← 白名单那版会漏掉它
 *   web/xx.config.ts           触发    ✓  ← 顶层新配置默认算数，这就是改黑名单的收益
 *   web/src/xx.test.ts         不触发  ✓  ← 要两条排除项，见下
 *   web/src/nodes/xx.test.ts   不触发  ✓
 *   web/src/xx.pem             不触发  ✓  ┐ 作用域**内**而被 .gitignore 忽略
 *   web/src/.DS_Store          不触发  ✓  ┘ —— 这两条才验得了「--porcelain 尊重 gitignore」
 *   web/mocks/xx.ts            不触发  ✓  dev-only，生产构建里没有
 *   web/tests/xx.ts            不触发  ✓
 *   web/scripts/xx.mjs         不触发  ✓
 *   request-shapes.json        不触发  ✓  拿它当时正 modified 的真实状态验的
 *
 * **两种结果都要出现**，否则说明探针本身坏了：一个永远匹配不上的探测会给出
 * 清一色的"不触发"，而那跟"排除项全生效"长得一模一样 —— 这一族错误没有一个
 * "零"可以看，不像"0 条测试跑了"那么显眼。
 *
 * ## 有一条探测我删了，因为它什么都没验到
 *
 * 原来这张表里有 `web/dist/xx.js → 不触发`，注解写着「.gitignore 挡着，
 * 构建产物不误伤」。**那个归因是错的**：`web/dist` 根本不在 SCOPE 里，
 * 它不触发是因为**不在作用域内**，跟 gitignore 一点关系都没有。两个原因
 * 各自都足以让它不触发，所以那条探测对「gitignore 起没起作用」零信息。
 *
 * > **一条探测「通过」了，不代表它验的是你以为的那件事** —— 它可能因为一个
 * > 完全无关的原因通过，而通过的样子一模一样。
 *
 * 换成了 `web/src/xx.pem` 和 `web/src/.DS_Store`：作用域**内**、被忽略，
 * 而同一个目录下的 `xx.ts` 触发 —— 有了这个对照，那句话才有依据。
 * （`*.pem` `*.key` `.DS_Store` 在 .gitignore 里都不带前导斜杠，任何目录都生效。）
 */
const SCOPE = [
  'web',
  // 只在 dev / 测试里跑，生产构建里没有它们（`check:dist` 每次验「没有 mock」）
  ':(exclude)web/mocks',
  ':(exclude)web/tests',
  ':(exclude)web/scripts',
  ':(exclude)web/playwright.config.ts',
  ':(exclude)web/vitest.config.ts',
  // 在 src 底下，但不进 import 图。两条：`**` 要求至少一层中间目录，
  // 直接躺在 src 下的 `model.test.ts` 匹配不上第二条。
  ':(exclude)web/*.test.ts',
  ':(exclude)web/**/*.test.ts',
  // 由 `pnpm gen:requests` 从 requests.ts 生成，给后端读的，不进产物
  ':(exclude)web/request-shapes.json',
]

/**
 * 作用域内有没有**没提交的**改动 —— 已跟踪的改动和新文件都算。
 *
 * 加这个是因为反过来那种错更坏：这个包一度戳着一个干净的 commit，而里面装着
 * 一整套那个 commit 上根本不存在的功能（`EditNodeModal.vue` 当时连文件都还没
 * 提交）。**说脏而实际干净，让人白找一遍；说干净而实际脏，让人拿错的东西上线。**
 *
 * 原来那条自检（「版本.txt 里的 commit 是 sha」）挡不住它：它比的是同一个变量，
 * 验的是「我写进去的等于我算出来的」，而不是「产物真的对应那个 commit」——
 * 在脏工作树上必然通过。
 */
const dirty = sh('git', ['status', '--porcelain', '--', ...SCOPE]) !== ''
const stamp = dirty ? `${sha}-dirty` : sha

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
commit  ${stamp}
built   ${built}
${
  dirty
    ? `
⚠ 这个包**不对应任何一个 commit**。打包时工作树里有未提交的改动，
  内容比 ${sha} 多（或少）一些东西，而那些东西不在版本库里。
  出了问题没法靠 commit 号复现出这一版 —— 正式发布前先提交再重打。
`
    : ''
}
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
const tgz = join(OUT_DIR, `edge-console-${stamp}.tar.gz`)
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
  if (!ver.includes(`commit  ${stamp}`)) problems.push(`版本.txt 里的 commit 不是 ${stamp}`)
} else {
  problems.push('包里没有 版本.txt')
}
rmSync(check, { recursive: true, force: true })

/* ── 校验和 ─────────────────────────────────────────────────────────── */

if (problems.length === 0) {
  const sum = sh('shasum', ['-a', '256', `edge-console-${stamp}.tar.gz`], OUT_DIR)
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
console.log(`    ${all.length} 个文件 · commit ${stamp}`)
console.log(`    顶层 ${TOP}/ · 属主 root/root · 目录 755 / 文件 644`)
console.log(`    归档对解包目标目录没有任何意见（模式和属主都不碰）`)
console.log(`    解包后与 web/dist 逐字节一致，没有 mock`)
console.log('')
process.exit(0)
