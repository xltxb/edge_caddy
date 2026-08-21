#!/usr/bin/env node
/**
 * 改坏探针 —— 每条改坏都要回答三个问题，不是一个。
 *
 * 1. 改动**真的落进文件了吗**（`str.replace` / `perl -pi` 不匹配也不报错）
 * 2. 目标测试**真的跑了吗**（改坏若造成语法错误，整个文件加载失败，
 *    一条测试都没执行 —— 那时「一条都没红」和「测试拦不住」产生同一个观测）
 * 3. 测试**红了吗**
 * 4. **红的是不是我想验的那一条**
 *
 * 第三问是后端撞出来的：它把探针放早了一步，测试红了 —— 但红在另一条断言上。
 * 「我加的那条拦住了」和「旧的那条先炸了」在终端上**都是一片红**。
 *
 * 只看第二问的话，两种不同的世界会产生同一个观测：
 *   - 改坏没生效 + 测试本来就绿  → 绿（我上一轮踩的）
 *   - 改坏生效了 + 打偏了        → 红（后端这轮踩的）
 * 两个都会被「看一眼红不红」放过去。
 *
 *   node scripts/probe.mjs
 */
import { readFileSync, writeFileSync, copyFileSync, unlinkSync } from 'node:fs'
import { execSync } from 'node:child_process'

/**
 * 每条：改哪个文件、把什么换成什么、跑哪个测试、预期哪条红、**它守的是什么**。
 *
 * `invariant` 那个字段是后端加的，我照抄：探针失败的时候人要立刻知道这是在拦
 * 什么，而不是去读那段被改坏的代码反推。
 */
const PROBES = [
  {
    name: '把下线揉进 status 那一格',
    invariant: '下线（意图）与离线（观察）各占一格，永不合并',
    file: 'src/nodes/flags.ts',
    from: '  if (n.drainedAt) {',
    to: "  if (n.drainedAt && n.status === 'down') {",
    spec: 'src/nodes/flags.test.ts',
    expect: '已下线且在线',
  },
  {
    name: '去掉「没同步」的降级，退回撒谎版',
    invariant: '解析安排没到服务商时，「已退出解析」是常驻的谎',
    file: 'src/nodes/flags.ts',
    from: "      text: dnsSyncOk === false ? '已标记退出（解析未变）' : '已退出解析',",
    to: "      text: '已退出解析',",
    spec: 'src/nodes/flags.test.ts',
    expect: '没同步',
  },
  {
    name: '「还没问到」也降级 —— 因为自己没问到就说节点在撒谎',
    invariant: '自己没问到不等于节点在撒谎 —— 宁可少说一句',
    file: 'src/nodes/flags.ts',
    from: 'dnsSyncOk === false ?',
    to: 'dnsSyncOk !== true ?',
    spec: 'src/nodes/flags.test.ts',
    expect: '还没问到',
  },
  {
    name: '解析闸门拦错方向，把「暂停解析」也拦掉',
    invariant: '只拦「开」的方向；拦「关」会让人没法暂停解析',
    file: 'src/nodes/flags.ts',
    from: "  if (n.dnsEnabled) return { ok: true, reason: '' } // 这是「关」的方向\n",
    to: '',
    spec: 'src/nodes/flags.test.ts',
    expect: '关」的方向',
  },
  /*
   * 这一条第一版的改坏是把 `if (drainedAt)` 换成 `if (offline)`，我据此
   * 声称「人为下线 + 已离线」那条被保护住了。**探针当场证明我打偏了**：
   * 那个改法下，一台已下线且离线的机器仍然走进 drained 分支，输出碰巧一样，
   * 我想验的那条**自始至终是绿的**；红的是另外两条。
   *
   * 真正的回归是退回**原来那个二选一**：只凭 status 在「自动」和「手动」之间挑。
   * 那才会让「已下线且离线」被说成是系统干的 —— 也就是那条测试的名字讲的事。
   */
  {
    name: 'DNS 归因退回原来那个二选一（只凭 status 判自动/手动）',
    invariant: '人做的事不能归给系统 —— 归错因的人会照着错方向查',
    file: 'src/dns/participation.ts',
    fromRe: /if \(dnsEnabled\) return \{ kind: 'active' \}/,
    to:
      "if (dnsEnabled) return { kind: 'active' }\n" +
      "  return offline\n" +
      "    ? { kind: 'paused', text: '离线，已自动退出解析', hint: '' }\n" +
      "    : { kind: 'paused', text: '已手动暂停解析', hint: '' }",
    spec: 'src/dns/participation.test.ts',
    expect: '人为下线 + 已离线',
  },
  {
    name: '渲染器空转（装置失效）—— 否定断言该被正面对照挡住',
    invariant: '否定断言必须有同一测试里的正面对照，装置失效要能被抓住',
    file: 'src/workbench/readable.ts',
    fromRe: /(export function routeReadable\([^)]*\)[^{]*\{)/,
    to: '$1\n  return {} as never',
    spec: 'src/workbench/readable.test.ts',
    expect: '白名单为空时同样不渲染拦截段',
  },
  {
    name: '删掉 !res.ok 的抛出 —— 404 会被静默吞成 null',
    invariant: 'HTTP 不 ok 就得抛，哪怕包裹体里 code 是 0',
    file: 'src/api/http.ts',
    from:
      '  if (!res.ok) {\n    throw new ApiError(res.status, payload.msg || `请求失败（HTTP ${res.status}）`)\n  }\n',
    to: '',
    spec: 'src/api/http.test.ts',
    expect: '404 且 code 为 0',
  },
]

let bad = 0
for (const p of PROBES) {
  const bak = `/tmp/probe-${p.file.replace(/\W/g, '_')}.bak`
  copyFileSync(p.file, bak)
  try {
    const src = readFileSync(p.file, 'utf8')
    let next
    if (p.fromRe) {
      if (!p.fromRe.test(src)) throw new Error('改坏的模式没命中')
      next = src.replace(p.fromRe, p.to)
    } else {
      if (!src.includes(p.from)) throw new Error('改坏的原文没命中')
      next = src.replace(p.from, p.to)
    }
    writeFileSync(p.file, next)

    // 第一问：改动真的落进文件了吗
    const after = readFileSync(p.file, 'utf8')
    if (after === src) throw new Error('写回之后文件没变')

    // 第二 + 第三问：红了吗、红的是不是那一条
    let out = ''
    try {
      out = execSync(`npx vitest run ${p.spec} 2>&1`, { encoding: 'utf8' })
    } catch (e) {
      out = String(e.stdout ?? '') + String(e.stderr ?? '')
    }
    const reds = [...out.matchAll(/^\s+×\s+(.+?)(?:\s+\d+ms)?$/gm)].map((m) => m[1].trim())
    const greens = [...out.matchAll(/^\s+✓\s+(.+?)(?:\s+\d+ms)?$/gm)].map((m) => m[1].trim())

    /*
     * 先证明**目标测试真的跑了**。
     *
     * 改坏若造成语法错误，整个 spec 文件加载失败，一条测试都不会执行 —— 那时
     * 「一条都没红」和「测试拦不住」产生**同一个观测**，而结论完全相反：前者是
     * 探针坏了，后者是测试坏了。没有这一问，我会去改一条本来没问题的断言。
     */
    const ran = [...reds, ...greens]
    if (ran.length === 0) {
      throw new Error(
        '一条测试都没执行 —— 多半是改坏造成了语法/加载错误，探针本身坏了，不是测试拦不住\n' +
          `      ${out.split('\n').filter((l) => /Error|error:/.test(l)).slice(0, 2).join('\n      ')}`,
      )
    }
    if (!ran.some((r) => r.includes(p.expect))) {
      throw new Error(`目标测试没出现在这次运行里（改名了？被 skip 了？）：预期含「${p.expect}」`)
    }
    if (reds.length === 0) throw new Error('目标测试跑了，但一条都没红 —— 测试拦不住这个改坏')
    const hit = reds.some((r) => r.includes(p.expect))
    if (!hit) {
      throw new Error(
        `打偏了：红的是 [${reds.join(' | ')}]，而预期含「${p.expect}」\n` +
          `      红色只说明「有东西拦住了」，不说明「我想验的那条拦住了」`,
      )
    }
    console.log(`✓ ${p.name}\n    红 ${reds.length} 条，命中：${reds.find((r) => r.includes(p.expect))}`)
  } catch (e) {
    bad += 1
    console.error(`✗ ${p.name}\n    守的是：${p.invariant}\n    ${e.message}`)
  } finally {
    copyFileSync(bak, p.file)
    try {
      unlinkSync(bak)
    } catch {
      /* 清不掉不影响结论 */
    }
  }
}
/*
 * 收尾自检：**证明探针没把源码改残**。
 *
 * 每条都在 finally 里还原了，但进程被杀、磁盘写失败、或者我哪天写错了还原逻辑，
 * 损伤会留在工作区里 —— 而那时后面所有测试跑的都不是真正的源码。
 * 一个会留下损伤的探针比没有探针更糟：它会让**别的**结论也变得不可信。
 */
const touched = [...new Set(PROBES.map((p) => p.file))]
let dirty = ''
try {
  dirty = execSync(`git diff --name-only -- ${touched.join(' ')}`, { encoding: 'utf8' }).trim()
} catch (e) {
  dirty = `（git 查不了：${e instanceof Error ? e.message : e}）`
}
if (dirty) {
  console.error(`\n✗ 探针改过的文件没还原干净：\n${dirty}\n  用 git checkout 还原之后再看上面的结论。\n`)
  process.exit(2)
}

console.log('')
if (bad) {
  console.error(`${bad} / ${PROBES.length} 条探针没能证明它想证的事。\n`)
  process.exit(1)
}
console.log(`${PROBES.length} 条探针全部命中预期断言。\n`)
