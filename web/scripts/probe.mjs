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
/*
 * 自检模式：`node scripts/probe.mjs --self-test`
 *
 * 这个脚本的四问此前是我**手工**验的 —— 手工跑一遍然后忘掉，等于没有，
 * 跟我把改坏固化成脚本之前的状态一模一样。后端指出这一点，照做。
 *
 * 下面每一条都是一个**刻意坏掉的探针**，跑它，断言脚本给出正确的判词。
 * 这五种失败**全都是写这个脚本的过程里真实撞上的**，不是设想出来的：
 *
 *   - 改坏没命中原文        （我第一次验「重试中不标」时踩的，结论碰巧对了）
 *   - 目标测试一条都没跑    （改坏造成语法错误，我上一轮加第二问的起因）
 *   - 改坏了但一条都没红    （测试确实拦不住）
 *   - 红了但红在别处        （我验 DNS 归因那条时踩的，想验的那条自始至终是绿的）
 *   - 还原不干净            （我为了验它故意制造过一次，结果 link.ts 差点被带进提交）
 *
 * 「这只是把问题推远一格」—— 在这一层可以停。产品代码的失败模式无穷，
 * 而这个脚本只有四问、每问一种失败方式，**枚举得完的东西封顶是完备的**。
 *
 * **靶子挑的是 `flags.test.ts`，而它必须是「已知被覆盖」的那种。**
 * 第三、第四条自检要验的是「脚本认不认得出没红 / 打偏」，而如果靶子那条测试
 * 本来就没覆盖住被改的东西，验到的就成了「没覆盖」——第四条我第一版正是这么
 * 错的：选了删掉 drift 旗标，而那条根本没有测试，结果是「一条都没红」。
 * flags.test.ts 是刚写的、每条都用探针验过改坏会红，所以拿它当靶子是有根据的，
 * 不是因为它跑得快。（这条判据来自后端 —— 它承认自己选靶时是运气。）
 */
const SELF_TESTS = [
  {
    what: '改坏没命中原文',
    probe: {
      name: '(自检) 原文写错',
      invariant: '改坏必须真的落进文件',
      file: 'src/nodes/flags.ts',
      from: '这段原文在文件里根本不存在',
      to: 'x',
      spec: 'src/nodes/flags.test.ts',
      expect: '已下线且在线',
    },
    expectMsg: '没命中',
  },
  {
    what: '一条测试都没跑（改坏造成语法错误）',
    probe: {
      name: '(自检) 语法错误',
      invariant: '目标测试必须真的执行过',
      file: 'src/nodes/flags.ts',
      from: '  if (n.drainedAt) {',
      to: '  if (n.drainedAt &&&& {',
      spec: 'src/nodes/flags.test.ts',
      expect: '已下线且在线',
    },
    expectMsg: '一条测试都没执行',
  },
  {
    what: '改坏无害，一条都没红',
    probe: {
      name: '(自检) 无害改动',
      invariant: '测试必须拦得住这个改坏',
      file: 'src/nodes/flags.ts',
      from: 'export interface NodeFlag {',
      to: '/* 无害注释 */\nexport interface NodeFlag {',
      spec: 'src/nodes/flags.test.ts',
      expect: '已下线且在线',
    },
    expectMsg: '一条都没红',
  },
  {
    what: '红了，但红在别处',
    probe: {
      name: '(自检) 打偏',
      invariant: '红的必须是想验的那一条',
      file: 'src/nodes/flags.ts',
      // 改「已退出解析」那句：它会让「同步过」那条红，而「已下线且在线」照样绿。
      // 期望却写成后者 —— 于是脚本必须说「打偏了」，而不是「通过」。
      // 第一版我选的改坏是删掉 drift 旗标，而那条根本没有测试覆盖，
      // 结果是「一条都没红」——**自检自己也会打偏**，同一个形状。
      from: "      text: dnsSyncOk === false ? '已标记退出（解析未变）' : '已退出解析',",
      to: "      text: dnsSyncOk === false ? '已标记退出（解析未变）' : '退出了解析',",
      spec: 'src/nodes/flags.test.ts',
      expect: '已下线且在线',
    },
    expectMsg: '打偏了',
  },
]

function probesToRun() {
  return PROBES
}

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
  /*
   * 从**名字**推的改坏：怎么让「只有 live 说得出实时」这句话为假？
   * —— 让别的状态也说「实时」。这是这个应用里最贵的一句假话：说错了，
   * 屏幕上每个数字都变成「陈旧但看着合理」。
   */
  {
    name: '让重连中也说「实时」',
    invariant: '只有 live 有资格断言屏幕上的东西是刚刚发生的',
    file: 'src/stores/link.ts',
    from: "      return { text: '重连中', tone: 'warn' }",
    to: "      return { text: '实时', tone: 'warn' }",
    spec: 'src/stores/link.test.ts',
    expect: '只有 live 说得出',
  },
  {
    name: '降级只换颜色，不用字说',
    invariant: '颜色是最容易丢的一路信息，降级必须有字',
    file: 'src/stores/link.ts',
    from: "      return { text: '实时已断 · 降为 2s 轮询', tone: 'danger' }",
    to: "      return { text: '实时', tone: 'danger' }",
    spec: 'src/stores/link.test.ts',
    expect: '降为轮询时文案里要有字说明',
  },
  /*
   * 从名字推的改坏：怎么让「没有同比数据时不暗示它会自己好起来」为假？
   * —— 把「历史不足」那句话放回去。它是契约里的说法，而那个 null 是永久的。
   */
  {
    name: '把「历史不足」放回同比脚注',
    invariant: '一个永久的空态不能说自己是暂时的 —— 那会让人明天再来看一眼',
    file: 'src/overview/kpis.ts',
    from: "      foot: k.connsDeltaPct === null ? '暂无同比数据' :",
    to: "      foot: k.connsDeltaPct === null ? '历史不足，暂无同比' :",
    spec: 'src/overview/kpis.test.ts',
    expect: '没有同比数据时不暗示它会自己好起来',
  },
  {
    name: '把在线数改成含 warn',
    invariant: '在线数与脚注「异常 N 个」必须对得上账，不能同一台机器算两次',
    file: 'src/overview/kpis.ts',
    from: '      value: `${k.nodesOnline}/${k.nodesTotal}`,',
    to: '      value: `${k.nodesOnline + k.nodesWarn}/${k.nodesTotal}`,',
    spec: 'src/overview/kpis.test.ts',
    expect: '在线数 + 异常 + 离线 = 总数',
  },
]

/*
 * 跑之前先拍一份快照。
 *
 * 第一版这里用的是 `git diff --name-only` —— 而那问的是「文件脏不脏」，
 * 我想问的是「**探针有没有把它还原成找到时的样子**」。两者在文件本来就有
 * 未提交改动时会分岔：那次它报了「没还原干净」，而探针其实干得很好。
 *
 * 又是同一族：用一个近似的观测代替真正的问题。快照比对问的正是那个问题，
 * 而且不依赖 git 装没装、在不在仓库里。
 */
const touched = [...new Set(PROBES.map((p) => p.file))]
const before = new Map(touched.map((f) => [f, readFileSync(f, 'utf8')]))

/**
 * 跑一条探针，返回判词。
 *
 * 抽成函数是为了让 `--self-test` 能复用它 —— 自检必须验的是**真正会跑的那份
 * 逻辑**，复制一份出来验等于没验。
 */
function runProbe(p) {
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

    let out = ''
    try {
      // --reporter=verbose：默认 reporter 在**全绿**时只打印文件级的一行
      // 「✓ flags.test.ts (10 tests)」，不打印每条测试名。于是第二问（目标测试
      // 跑了吗）在全绿时永远拿不到名字 —— 而全绿正是第三问该开火的场合。
      // 这个洞是 --self-test 抓出来的，不是我看出来的。
      out = execSync(`npx vitest run --reporter=verbose ${p.spec} 2>&1`, { encoding: 'utf8' })
    } catch (e) {
      out = String(e.stdout ?? '') + String(e.stderr ?? '')
    }
    const reds = [...out.matchAll(/^\s+×\s+(.+?)(?:\s+\d+ms)?$/gm)].map((m) => m[1].trim())
    const greens = [...out.matchAll(/^\s+✓\s+(.+?)(?:\s+\d+ms)?$/gm)].map((m) => m[1].trim())

    /*
     * 第二问：**目标测试真的跑了吗**。
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
    // 第三问：红了吗
    if (reds.length === 0) throw new Error('目标测试跑了，但一条都没红 —— 测试拦不住这个改坏')
    // 第四问：红的是不是那一条
    if (!reds.some((r) => r.includes(p.expect))) {
      throw new Error(
        `打偏了：红的是 [${reds.join(' | ')}]，而预期含「${p.expect}」\n` +
          `      红色只说明「有东西拦住了」，不说明「我想验的那条拦住了」`,
      )
    }
    return { ok: true, detail: `红 ${reds.length} 条，命中：${reds.find((r) => r.includes(p.expect))}` }
  } catch (e) {
    return { ok: false, detail: e instanceof Error ? e.message : String(e) }
  } finally {
    copyFileSync(bak, p.file)
    try {
      unlinkSync(bak)
    } catch {
      /* 清不掉不影响结论 */
    }
  }
}

/** `--self-test`：用刻意坏掉的探针跑一遍，断言脚本给出正确的判词。 */
function runSelfTest() {
  let bad = 0
  for (const t of SELF_TESTS) {
    const r = runProbe(t.probe)
    if (r.ok) {
      bad += 1
      console.error(`✗ 自检「${t.what}」：脚本说它通过了 —— 这一问已经失效`)
    } else if (!r.detail.includes(t.expectMsg)) {
      bad += 1
      console.error(
        `✗ 自检「${t.what}」：判词不对\n    预期含「${t.expectMsg}」，实际：${r.detail.split('\n')[0]}`,
      )
    } else {
      console.log(`✓ 自检「${t.what}」→ ${r.detail.split('\n')[0]}`)
    }
  }
  console.log('')
  if (bad) {
    console.error(`${bad} / ${SELF_TESTS.length} 条自检没过 —— **上面那些探针的结论都不作数**。\n`)
    process.exit(3)
  }
  console.log(`${SELF_TESTS.length} 条自检全过：四问都还拦得住它们各自要拦的那种失败。\n`)
  process.exit(0)
}

if (process.argv.includes('--self-test')) runSelfTest()

let bad = 0
for (const p of probesToRun()) {
  const r = runProbe(p)
  if (r.ok) console.log(`✓ ${p.name}\n    ${r.detail}`)
  else {
    bad += 1
    console.error(`✗ ${p.name}\n    守的是：${p.invariant}\n    ${r.detail}`)
  }
}

/*
 * 收尾自检：**证明探针没把源码改残**。
 *
 * 每条都在 finally 里还原了，但进程被杀、磁盘写失败、或者我哪天写错了还原逻辑，
 * 损伤会留在工作区里 —— 而那时后面所有测试跑的都不是真正的源码。
 * 一个会留下损伤的探针比没有探针更糟：它会让**别的**结论也变得不可信。
 */
const dirty = touched.filter((f) => readFileSync(f, 'utf8') !== before.get(f)).join('\n')
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
