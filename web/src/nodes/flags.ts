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
 * 「恢复解析」能不能按。
 *
 * 已下线的节点开解析后端回 2001。置灰而不是让人点了再被拒 —— **一道人人都会
 * 撞到的拒绝，说明那个按钮不该能按**。关解析不拒，所以只在「要开」的方向拦。
 */
export function canEnableDns(n: EdgeNode): { ok: boolean; reason: string } {
  if (n.dnsEnabled) return { ok: true, reason: '' } // 这是「关」的方向
  if (n.drainedAt) return { ok: false, reason: '该节点已被下线，先「重新上线」再恢复解析' }
  return { ok: true, reason: '' }
}
