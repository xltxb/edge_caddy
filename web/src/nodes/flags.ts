/**
 * 节点行上那些**断言**，抽成纯函数。
 *
 * 抽出来不是为了复用 —— 只有一处用它。是为了**能被证伪**：这几句话每一句都是
 * 对现实的断言（「这台机器不接流量了」「这是人关的」），而写在模板里的断言，
 * 只有人盯着截图看的时候才会被检查一次。
 *
 * 后端在 ADR-0014 上撞到过更狠的一层：**一份论证的结论会被测试保护，论证本身
 * 不会。** 它按论证分了两列，而「心跳会冲掉同一列」这个前提没人验 —— 给心跳的
 * SQL 补一句 `drained_at = NULL`（注释写「节点回来了就清掉下线标记」，听起来
 * 完全合理），191 个测试里一条都不红。下面每个函数对应的测试，都是照这个办法
 * 反过来验的：故意改坏，确认真的会红。
 */

import type { EdgeNode } from '@/model'

export interface NodeFlag {
  text: string
  tone: 'muted' | 'warn'
  title?: string
}

/**
 * 行上的旗标。**不含 status** —— 那一格由 VStatusPill 单独占着。
 *
 * `status` 是**观察**（主控连着几个周期没收到心跳），`drainedAt` 是**意图**
 * （人按了下线）。一台节点可以「已下线且在线」，也可以「未下线但离线」；
 * 前者是「我关的」，后者是故障。**合成一格，运维半夜分不清该不该起床**
 * （CONTEXT.md、ADR-0014）。
 */
export function nodeFlags(n: EdgeNode, dnsSyncOk: boolean | null): NodeFlag[] {
  const out: NodeFlag[] = []
  if (n.drainedAt) {
    out.push({ text: '已下线（人为）', tone: 'muted', title: `下线于 ${n.drainedAt}` })
  }
  if (!n.dnsEnabled) {
    // 解析安排没到服务商那边时，「已退出解析」是**常驻的谎** —— 标志位改了，
    // 那台机器照旧在解析里。dnsSyncOk 为 null 表示还没问到，那就不加限定：
    // 宁可少说一句，也不要因为自己没问到就反过来说节点在撒谎。
    out.push({
      text: dnsSyncOk === false ? '已标记退出（解析未变）' : '已退出解析',
      tone: 'warn',
    })
  }
  if (n.drift) out.push({ text: '未收到最近下发', tone: 'warn' })
  return out
}

/**
 * 解析开关能不能按。
 *
 * 已下线的节点**两个方向都会被拒**（契约 §4：`2001`）。置灰而不是让人点了
 * 再被拒 —— **一道人人都会撞到的拒绝，说明那个按钮不该能按**。
 *
 * 「关」的方向原先是放行的，理由是「它不会把流量送到一台连不上的机器上」——
 * **那个理由至今成立，它只是漏了一件事**：关这一下会把 `dns_reason` 改写成
 * `manual`，而 `drained_at` 还在。于是节点页说「已下线」、DNS 页说「人手动
 * 关的」，两页各说各的，而**每一句单独看都是对的**。
 *
 * 所以这不是「把约束改严了」。两者在 diff 里长得一样（一条断言反过来了），
 * 但该写进提交的话不同：观测能力变了要写「它为什么**现在**可以更松」，
 * 发现旧约束的代价要写「原来那条**漏了什么**」。这是后一种。
 */
export function canToggleDns(n: EdgeNode): { ok: boolean; reason: string } {
  if (!n.drainedAt) return { ok: true, reason: '' }
  return {
    ok: false,
    reason: n.dnsEnabled
      ? '该节点已被下线，本来就不在解析里'
      : '该节点已被下线，先「重新上线」再恢复解析',
  }
}

/**
 * 能不能删这条记录。**必须先下线**，与 `canToggleDns` 同一个理由置灰：
 * 一道人人都会撞到的拒绝，说明那个按钮不该能按。
 *
 * **但这一条和「危险操作二次确认」是两种东西**，别照那个写措辞。
 *
 * 一台还连着的机器手里有隧道证书。删掉记录之后它会重连、被按证书认出来、
 * 然后在一张不存在的行上写心跳 —— `TouchHeartbeat` 是 UPDATE，影响 0 行，
 * **不报错**。结果是一台连着、在服务、而控制台上看不见的机器。下线会断隧道
 * 并拒绝它重连（ADR-0014），所以下线是这个操作**真正的前提**。
 *
 * 判据（后端 `docs/agents/domain.md`）：
 * **去掉这个检查会产生不一致的状态 → 那是前提；只是「后果严重」→ 那才是确认。**
 * 前提要说「先做那件事」，确认要说「你确定吗」——写反了，人会以为自己在被
 * 劝阻，然后找地方跳过它。
 */
export function canDelete(n: EdgeNode): { ok: boolean; reason: string } {
  if (n.drainedAt) return { ok: true, reason: '' }
  return {
    ok: false,
    reason: '先下线这台节点再删。它现在还连着，删掉记录它会重连并在一张不存在的行上写心跳',
  }
}
