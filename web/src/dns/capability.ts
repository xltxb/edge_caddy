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

/**
 * 这个合并组上参与解析的节点权重加起来是不是 0。
 *
 * **权重全 0 = 这条解析线路没有任何节点承载流量。** 而旁边那句
 * 「N / M 个节点参与解析」读起来像它在正常工作 —— 两句都对，只说前一句
 * 会让人以为配好了。
 *
 * 这个状态是**常态而不是异常**：节点接入后会出现在全部五条解析线路上、
 * 权重 0，那就是给它配权重的入口（契约 §8）。后端此前只列「已经在权重表里」
 * 的节点，而写那张表的唯一入口就是那一页 —— 新接入的节点永远进不了解析，
 * 而页面看上去完全正常。
 *
 * 只数**参与解析**的：一台被暂停的机器权重还留着，但它不承载流量，
 * 把它算进来会让一条实际上没有出口的线路看起来有出口。
 */
export function isIdle(
  weights: Record<string, Record<string, number>>,
  group: LineInput,
  nodes: { id: string; dnsEnabled: boolean }[],
): boolean {
  if (!nodes.length) return false
  return nodes
    .filter((n) => n.dnsEnabled)
    .reduce((sum, n) => sum + mergedWeight(weights, group, n.id), 0) === 0
}

/** 一个节点在某条（或某个合并组）线路上的现状，`computeShares` 只需要这些。 */
export interface ShareInput {
  node: string
  /**
   * **它在不在解析里 —— 直接用后端的 `in_rotation`，不要自己推。**
   *
   * 这里原先传的是 `dns_enabled`，而后端的判据是
   * `dns_enabled && status != down && weight > 0`（`dnssched.Build`）。
   *
   * 灰度上撞出来的：一台 `dns_enabled: true`、`status: ok`、权重 0 的机器，
   * 后端说 `in_rotation: false`（它真的不在解析里），
   * **而这一页给它画了 50.0%**。
   *
   * 两边各自都对 —— 而界面说的和服务商上的不是一回事。
   * 判据只该有一处，那一处在后端。
   *
   * `null`（算不出来）按 false 处理：一条画不出来的占比条，
   * 比一条画错的好。
   */
  enabled: boolean
  /** 库里配的权重值。**表达不了权重的服务商下这个字段会被忽略。** */
  weight: number
}

/**
 * 本地预览占比。**画的是将会发生的事，不是库里存着的数。**
 *
 * ## 表达不了权重时是均分，不是按权重算
 *
 * 这一条是我漏过一次的：把权重输入框换成「轮换」两个字之后，我以为就说清楚了
 * —— **而占比条还在按库里的权重画 60% / 40%**。那正是后端当初拒绝默默取等权时
 * 担心的东西：
 *
 * > 界面上权重条画着 60/40 而实际是轮询，
 * > **而那种不一致没有任何地方会说出来。**
 *
 * 库里那些权重仍然存着（换回 DNSPod 还要用），但普通 A / AAAA 记录只有
 * 「在或不在」，DNS 轮询把它们摆平。
 *
 * ## 退出解析的节点不进分母
 *
 * 与后端一致：它的权重仍然保留（人的意图），但不参与这一轮分流。
 */
export function computeShares(
  entries: ShareInput[],
  supportsWeights: boolean,
): Map<string, number> {
  const m = new Map<string, number>()
  const enabled = entries.filter((e) => e.enabled)

  if (!supportsWeights) {
    const each = enabled.length > 0 ? Math.round((100 / enabled.length) * 10) / 10 : 0
    for (const e of entries) m.set(e.node, e.enabled ? each : 0)
    return m
  }

  const total = enabled.reduce((sum, e) => sum + e.weight, 0)
  for (const e of entries) {
    m.set(e.node, e.enabled && total > 0 ? Math.round((e.weight / total) * 1000) / 10 : 0)
  }
  return m
}
