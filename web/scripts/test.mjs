#!/usr/bin/env node
/**
 * 跑全部检查，并且**保证失败不会被压成一个数字**。
 *
 * 为什么要这个脚本：我这一整轮都在用 `npx vitest run | grep -E "Tests +[0-9]"`
 * ——只看计数，细节全丢。这跟后端那一行只打印计数的 python 是同一个东西，
 * 而它那边的代价是：一次「失败=1」被跟着提交了，那个 1 是什么至今不知道，
 * 后来六轮都没复现。**现场没留下，那次运行就等于没跑过。**
 *
 * 我自己也有前科：`5bb2dd9` 是红着提交的，`417bbda` 才修。
 *
 * 所以这里的规矩：
 *
 * 1. **失败一定连细节一起打**（测试名、断言、堆栈），永远不只给计数
 * 2. **跑了多少条也要打** —— 「0 失败」在一条都没跑的时候同样成立，
 *    而那两种情况的处置完全相反
 * 3. **非零退出**，让 `&&` 链断在这里，而不是让下一条命令（比如 git commit）跑起来
 *
 *   node scripts/test.mjs [--fast]     # --fast 跳过 e2e
 */
import { spawnSync } from 'node:child_process'

const fast = process.argv.includes('--fast')

/** 每一项：怎么跑、怎么从输出里认出「确实跑了多少」。 */
const STEPS = [
  {
    name: '类型检查',
    cmd: 'npx',
    args: ['vue-tsc', '--noEmit'],
    // vue-tsc 没问题时**什么也不打印** —— 所以它的「跑过了」由退出码承担
    ranRe: null,
  },
  {
    name: '单元测试',
    cmd: 'npx',
    args: ['vitest', 'run'],
    ranRe: /Tests\s+(?:\d+ failed \| )?(\d+) passed/,
  },
  {
    name: '模板标记',
    cmd: 'node',
    args: ['scripts/check-templates.mjs'],
    ranRe: /(\d+) 个模板/,
  },
  {
    name: '后端理由的出处',
    cmd: 'node',
    args: ['scripts/check-anchors.mjs'],
    ranRe: /(\d+) 处用后端行为解释/,
  },
  {
    name: '复盘注释首句',
    cmd: 'node',
    args: ['scripts/check-comments.mjs'],
    ranRe: /(\d+) 个复盘块/,
  },
  {
    name: '界面文案里的 markdown',
    cmd: 'node',
    args: ['scripts/check-humantext.mjs'],
    ranRe: /(\d+) 个字符串/,
  },
  {
    /*
     * **对真主控比响应形状。连不上就跳过（exit 3），而跳过要说出来。**
     *
     * 这一步此前不在这张表里，于是 `check:shapes` 写完之后基本没跑过 ——
     * 而它本该抓到两个真 bug，两个方向各一个：
     *
     *   scope / key_type   前端声明了、后端从来没发过 → 界面上一直是空格子
     *   not_after          后端一直在发、前端不知道   → 连空格子都没有
     *
     * 它的判据是**双向**的（「一边有这个键、另一边压根没有」算硬分歧），
     * 两个都在射程里。没抓到不是判据窄，是**它没跑**。
     *
     * > **一个从没跑过的检查，和一句「等条件具备我跑一遍」的承诺没有区别。**
     *
     * 这句话就写在 `src/certs-assertions.test.ts` 顶上 —— 同一个形状，
     * 在同一个仓库里，被写下来之后又发生了一次。
     *
     * 另外两个要连主控的检查**故意不在这里**：`check:certs` 会真的 drain 一台
     * 节点，`check:premises` 有真写。它们对生产是破坏性的，不能挂在一条谁都会
     * 顺手跑的链上。`check:shapes` 只读（两个 PUT 是空 body 的 no-op），所以它可以。
     */
    name: '响应形状（对真主控）',
    cmd: 'node',
    args: ['scripts/check-shapes.mjs'],
    /** exit 3 = 连不上真主控。不是失败，但**必须打出来**，否则又是一次静默跳过。 */
    skipStatus: 3,
    ranRe: /(\d+) 个端点/,
    /*
     * **这一步的「N 条」不足以说清它这次验到了什么。**
     *
     * 它比的是真主控，而真主控有状态：一层数据为空（真库没有那种记录）、
     * 或者一层按 type 变形，那一层的字段就一个都没比到 —— 而端点数照样是 11。
     *
     * 那些盲区在 `check-shapes` 自己的输出里有 ⚠，可**看这条链的人多半只看
     * 这一行**。不带上来的话，「11 个端点全比过了」和「11 个端点里有 3 层
     * 压根没比到」在他的读法里是同一句话。
     */
    noteRe: /但有 (\d+) 处没比到/,
  },
  {
    // 不需要真主控：它只比源码与登记表，以及那份导给后端的 JSON 有没有过期
    name: '写请求的字段清单',
    cmd: 'node',
    args: ['scripts/check-requests.mjs'],
    ranRe: /(\d+) 处写请求/,
  },
  ...(fast
    ? []
    : [
        {
          name: '端到端',
          cmd: 'npx',
          args: ['playwright', 'test'],
          ranRe: /(\d+) passed/,
        },
      ]),
]

let bad = 0
/** 跳过的步骤名。**收尾那句必须点出来** —— 跳过与通过不能长成一个样子。 */
const skipped = []
for (const s of STEPS) {
  const r = spawnSync(s.cmd, s.args, { encoding: 'utf8' })
  const out = `${r.stdout ?? ''}${r.stderr ?? ''}`

  if (s.skipStatus !== undefined && r.status === s.skipStatus) {
    /*
     * **跳过不是通过。** 打一行独立的记号，并计进收尾那句 —— 一次没跑的检查
     * 和一次通过的检查，在「N 步全过」里长得一模一样，而今天撞到的每一个坑
     * 都是这个形状。
     */
    skipped.push(s.name)
    console.log(`⊘ ${s.name}：跳过 —— ${(out.trim().split('\n').pop() || '连不上真主控').trim()}`)
    continue
  }

  if (r.status !== 0) {
    bad += 1
    console.error(`\n✗ ${s.name} 失败（exit ${r.status}）——完整输出：\n`)
    console.error(out.trimEnd())
    console.error('')
    continue
  }

  /*
   * 退出码为 0 还不够。
   *
   * 「一条都没跑」和「全都过了」都是 exit 0 —— 配置写错、glob 匹配不到文件、
   * 测试被整体 skip，产生的观测跟一切正常一模一样。所以能数出条数的步骤，
   * 必须真的数出来。
   */
  if (s.ranRe) {
    /*
     * 先认「被整体跳过」这一种，再落到笼统的「认不出」。
     *
     * vitest 在**一条都没匹配上**时印「Tests 193 skipped (193)」并 **exit 0**
     * ——「跑了但全过」和「一条都没跑」在退出码上一模一样（playwright 那边
     * 是 exit 1，两个 runner 在这件事上不一致，都验过了）。
     *
     * 笼统的判词能拦住它，但会说「装置可能坏了」，把人指向工具而不是那个
     * 打错的 -t 参数。**判词指错方向，跟不报一样费时间**——这正是这一整轮
     * 在修的东西，没理由在这个脚本自己身上留一处。
     */
    if (/\bskipped\b/.test(out) && !/\d+ passed/.test(out)) {
      bad += 1
      console.error(
        `\n✗ ${s.name}：全部被跳过，一条都没执行（而退出码是 0）\n` +
          `      多半是 -t / --grep 的名字打错了，不是测试出问题\n`,
      )
      continue
    }

    const m = s.ranRe.exec(out)
    if (!m) {
      bad += 1
      console.error(`\n✗ ${s.name}：退出码是 0，但认不出跑了多少条 —— 装置可能坏了\n`)
      console.error(out.trimEnd().split('\n').slice(-15).join('\n'))
      continue
    }
    if (Number(m[1]) === 0) {
      bad += 1
      console.error(`\n✗ ${s.name}：一条都没跑，而退出码是 0 —— 这不是「全过了」\n`)
      continue
    }
    const note = s.noteRe ? s.noteRe.exec(out) : null
    console.log(`✓ ${s.name}：${m[1]} 条${note ? `（${note[1]} 处没比到）` : ''}`)
  } else {
    console.log(`✓ ${s.name}`)
  }
}

console.log('')
if (bad) {
  console.error(`${bad} / ${STEPS.length} 步没过。**不要在这个状态下提交。**\n`)
  process.exit(1)
}
const ran = STEPS.length - skipped.length
console.log(
  `${ran} / ${STEPS.length} 步通过${fast ? '（跳过了 e2e）' : ''}` +
    (skipped.length ? `，${skipped.length} 步跳过：${skipped.join('、')}` : '') +
    '。\n',
)
