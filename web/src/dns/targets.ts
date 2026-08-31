/**
 * 解析域名的两种形态之间的翻译。
 *
 * - **线上形态**是平铺的 `targets`（契约 §11）：每条 = `domain + sub + zone_id`，
 *   `PUT` 整份替换。
 * - **界面形态**按顶级域名分组：`zone_id` 挂在组上 —— 一个域名只有一个 zone，
 *   契约自己也说「同一 zone 下的多个子域填同一个」。逐行填它只是平铺形态的
 *   副产品，不是它有每行不同的自由。
 *
 * 保存时发的仍然是平铺整份，所以这里翻译错一条 = 服务商那边少写或多写一条记录。
 */

import type { DnsTargetWire } from '@/api/types'

export interface DomainGroup {
  domain: string
  /** 组级 Zone ID（仅 Cloudflare 用）：摊平时写进该组每一条。 */
  zoneId: string
  /** 子域前缀列表，空串 = 根。 */
  subs: string[]
}

export interface GroupedTargets {
  groups: DomainGroup[]
  /**
   * 同一域名带了**多个不同的非空** zone_id 的域名。统一取第一个非空值，
   * 但要说出来 —— 静默统一的话，人对着服务商后台那条对不上的记录没处查起。
   * 空串混着一个有值的不算：那是没填完，不是填岔了。
   */
  zoneConflicts: string[]
}

/** 平铺 targets 按顶级域名收拢。组序 = 域名第一次出现的顺序，子域保序去重。 */
export function groupTargets(flat: DnsTargetWire[]): GroupedTargets {
  const byDomain = new Map<string, DomainGroup>()
  const conflicts = new Set<string>()

  for (const t of flat) {
    let g = byDomain.get(t.domain)
    if (!g) {
      g = { domain: t.domain, zoneId: '', subs: [] }
      byDomain.set(t.domain, g)
    }
    // 重复的（域名, 子域）只留一条 —— 摊平回去时它会变成两条一样的记录。
    if (!g.subs.includes(t.sub)) g.subs.push(t.sub)
    if (t.zone_id) {
      if (!g.zoneId) g.zoneId = t.zone_id
      else if (g.zoneId !== t.zone_id) conflicts.add(t.domain)
    }
  }

  return { groups: [...byDomain.values()], zoneConflicts: [...conflicts] }
}

/**
 * 分组摊平回线上形态：每个子域一条，`zone_id` 用组上那份。
 *
 * 平铺形态里「0 条记录的域名」不存在 —— 没有行就没有域名，真摊平成 0 条
 * 这个域名会在保存后**无声消失**。界面守着「新域名默认给根记录」，
 * 这里兜底：没有子域就当只有根。
 */
export function flattenGroups(groups: DomainGroup[]): DnsTargetWire[] {
  return groups.flatMap((g) => {
    const subs = g.subs.length ? g.subs : ['']
    return subs.map((sub) => ({ domain: g.domain, sub, zone_id: g.zoneId }))
  })
}

/**
 * 一次粘一串域名。人从记事本、表格里粘过来的是换行、逗号（中英文都有）、
 * 顿号、分号、空格分隔的一串 —— 都认。去重、去空白，保持出现顺序。
 */
export function parseDomainsInput(input: string): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const piece of input.split(/[\s,，、;；]+/)) {
    const d = piece.trim()
    if (d && !seen.has(d)) {
      seen.add(d)
      out.push(d)
    }
  }
  return out
}
