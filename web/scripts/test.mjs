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
for (const s of STEPS) {
  const r = spawnSync(s.cmd, s.args, { encoding: 'utf8' })
  const out = `${r.stdout ?? ''}${r.stderr ?? ''}`

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
    console.log(`✓ ${s.name}：${m[1]} 条`)
  } else {
    console.log(`✓ ${s.name}`)
  }
}

console.log('')
if (bad) {
  console.error(`${bad} / ${STEPS.length} 步没过。**不要在这个状态下提交。**\n`)
  process.exit(1)
}
console.log(`${STEPS.length} 步全过${fast ? '（跳过了 e2e）' : ''}。\n`)
