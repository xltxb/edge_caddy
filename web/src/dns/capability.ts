/**
 * 把「契约里的五条线路」映射到「服务商能表达的线路」。
 *
 * 契约 §8 的线路码固定五个（ct/cu/cm/tw/ov），但服务商未必都能表达。
 * Cloudflare 只有国家 / 大洲维度，电信 / 联通 / 移动在它那边是同一个「中国」。
 *
 * 界面据此**把表达不了的几条合并成一个输入框**，而不是分别列出再在保存时
 * 拒绝。合并让非法状态无法被表达 —— 比让人配完三个不同的数字、点保存、
 * 然后拿到一个拒绝要好：那是最差的告知时机。
 *
 * 覆盖关系（`covers`）由**后端**给。前端不持有任何服务商的地理模型，
 * 所以加第三家服务商时这里不用改。
 */

import type { DnsCapabilityLineWire } from '@/api/types'

export interface LineInput {
  code: string
  name: string
  /** 这一组实际覆盖的契约线路码。合并组会有多个。 */
  covers: string[]
  /** 服务商表达不了这一组时为 false —— 界面应当禁用并说明原因。 */
  supported: boolean
}

/**
 * 按服务商能力算出该渲染成几组输入。
 *
 * `capabilityLines` 为空（尚未配置服务商）时，按契约的五条线原样渲染 ——
 * 那时权重只是本地意图，没有服务商会拒绝它。
 */
export function lineInputs(
  contractLines: { code: string; name: string }[],
  capabilityLines: DnsCapabilityLineWire[] | null | undefined,
): LineInput[] {
  if (!capabilityLines || capabilityLines.length === 0) {
    return contractLines.map((l) => ({
      code: l.code,
      name: l.name,
      covers: [l.code],
      supported: true,
    }))
  }

  const groups: LineInput[] = []
  const covered = new Set<string>()

  for (const cap of capabilityLines) {
    // 只保留契约里真的存在的线路码：服务商报了别的，我们也画不出来
    const covers = (cap.covers ?? []).filter((c) => contractLines.some((l) => l.code === c))
    if (covers.length === 0) continue
    for (const c of covers) covered.add(c)
    groups.push({ code: cap.code, name: cap.name, covers, supported: true })
  }

  // 契约里有、但服务商一条都覆盖不到的线路：仍然列出来，但禁用。
  // 悄悄藏掉会让人以为这条线路不存在，而它在契约里是存在的。
  for (const l of contractLines) {
    if (!covered.has(l.code)) {
      groups.push({ code: l.code, name: l.name, covers: [l.code], supported: false })
    }
  }
  return groups
}

/** 合并组里某个节点的当前权重：取第一条被覆盖线路的值。 */
export function mergedWeight(
  weights: Record<string, Record<string, number>>,
  group: LineInput,
  node: string,
): number {
  for (const c of group.covers) {
    const w = weights[c]?.[node]
    if (w !== undefined) return w
  }
  return 0
}

/**
 * 合并组里各线路的权重是否已经分叉。
 *
 * 分叉说明这份配置是在能力更强的服务商下配的，换过来之后表达不了 ——
 * 要让人知道保存会把它们拉平，而不是默默取第一条的值。
 */
export function isDivergent(
  weights: Record<string, Record<string, number>>,
  group: LineInput,
): boolean {
  if (group.covers.length < 2) return false
  const nodes = new Set(group.covers.flatMap((c) => Object.keys(weights[c] ?? {})))
  for (const n of nodes) {
    const vals = group.covers.map((c) => weights[c]?.[n] ?? 0)
    if (vals.some((v) => v !== vals[0])) return true
  }
  return false
}
