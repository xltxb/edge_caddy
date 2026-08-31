import { describe, expect, it } from 'vitest'
import { flattenGroups, groupTargets, parseDomainsInput } from './targets'

/*
 * 线上形态是平铺的 `targets`（契约 §11：每条 = domain + sub + zone_id），
 * 界面形态是按顶级域名分组（zone_id 挂在组上 —— 契约本来就说同一 zone
 * 的多个子域填同一个）。这两个函数是两种形态之间的翻译，**保存时发的
 * 仍然是平铺的整份**，所以翻译错一条就是服务商那边少写或多写一条记录。
 */
describe('groupTargets —— 平铺 targets 按顶级域名收拢', () => {
  it('同一域名的多条收进一组，子域保序', () => {
    const { groups } = groupTargets([
      { domain: 'webjump.top', sub: 'cdn', zone_id: 'abc123' },
      { domain: 'webjump.top', sub: 'edge', zone_id: 'abc123' },
      { domain: 'other.com', sub: '', zone_id: 'def456' },
    ])
    expect(groups).toEqual([
      { domain: 'webjump.top', zoneId: 'abc123', subs: ['cdn', 'edge'] },
      { domain: 'other.com', zoneId: 'def456', subs: [''] },
    ])
  })

  it('组的顺序 = 域名第一次出现的顺序，交错的行也归回原组', () => {
    const { groups } = groupTargets([
      { domain: 'a.com', sub: '', zone_id: '' },
      { domain: 'b.com', sub: '', zone_id: '' },
      { domain: 'a.com', sub: 'cdn', zone_id: '' },
    ])
    expect(groups.map((g) => g.domain)).toEqual(['a.com', 'b.com'])
    expect(groups[0]!.subs).toEqual(['', 'cdn'])
  })

  /*
   * 旧数据一行一个 zone_id，理论上能塞出同域名不同 zone —— 那在服务商那边
   * 本来就是错的（一个域名只有一个 zone）。统一取第一个非空值，**但要把
   * 冲突说出来**：静默统一的话，人对着服务商后台那条对不上的记录没处查起。
   */
  it('同域名 zone_id 不一致：取第一个非空，并点名冲突的域名', () => {
    const { groups, zoneConflicts } = groupTargets([
      { domain: 'a.com', sub: '', zone_id: '' },
      { domain: 'a.com', sub: 'cdn', zone_id: 'zzz' },
      { domain: 'a.com', sub: 'edge', zone_id: 'yyy' },
    ])
    expect(groups[0]!.zoneId).toBe('zzz')
    expect(zoneConflicts).toEqual(['a.com'])
  })

  it('空 zone_id 混着一个有值的不算冲突 —— 那是没填完，不是填岔了', () => {
    const { zoneConflicts } = groupTargets([
      { domain: 'a.com', sub: '', zone_id: '' },
      { domain: 'a.com', sub: 'cdn', zone_id: 'zzz' },
    ])
    expect(zoneConflicts).toEqual([])
  })

  it('重复的（域名, 子域）只留一条 —— 摊平回去时它会变成两条一样的记录', () => {
    const { groups } = groupTargets([
      { domain: 'a.com', sub: 'cdn', zone_id: '' },
      { domain: 'a.com', sub: 'cdn', zone_id: '' },
    ])
    expect(groups[0]!.subs).toEqual(['cdn'])
  })
})

describe('flattenGroups —— 分组摊平回线上的平铺形态', () => {
  it('每个子域一条，zone_id 用组上那份', () => {
    expect(
      flattenGroups([{ domain: 'webjump.top', zoneId: 'abc123', subs: ['cdn', ''] }]),
    ).toEqual([
      { domain: 'webjump.top', sub: 'cdn', zone_id: 'abc123' },
      { domain: 'webjump.top', sub: '', zone_id: 'abc123' },
    ])
  })

  /*
   * 平铺形态里「0 条记录的域名」不存在 —— 没有行就没有域名。真让它摊平成
   * 0 条的话，这个域名会在保存后**无声消失**。守住这条是界面的事
   * （新加域名默认给根记录），这里兜底：没有子域就当只有根。
   */
  it('没有子域的组按「只有根」摊平，域名不会无声消失', () => {
    expect(flattenGroups([{ domain: 'a.com', zoneId: '', subs: [] }])).toEqual([
      { domain: 'a.com', sub: '', zone_id: '' },
    ])
  })

  it('分组再摊平回到原样（子域同 zone 的正常数据）', () => {
    const flat = [
      { domain: 'webjump.top', sub: 'cdn', zone_id: 'abc123' },
      { domain: 'webjump.top', sub: 'edge', zone_id: 'abc123' },
      { domain: 'other.com', sub: '', zone_id: 'def456' },
    ]
    expect(flattenGroups(groupTargets(flat).groups)).toEqual(flat)
  })
})

/*
 * 「方便一次加多个」落在输入的宽容度上：人从记事本、表格里粘过来的是
 * 换行、逗号（中英文都有）、空格分隔的一串 —— 都认。
 */
describe('parseDomainsInput —— 一次粘一串域名', () => {
  it('换行、逗号（中英文）、顿号、分号、空格都当分隔符', () => {
    expect(parseDomainsInput('a.com, b.com，c.com、d.com; e.com\nf.com g.com')).toEqual([
      'a.com',
      'b.com',
      'c.com',
      'd.com',
      'e.com',
      'f.com',
      'g.com',
    ])
  })

  it('去重、去空白，保持出现顺序', () => {
    expect(parseDomainsInput('  a.com \n\n a.com  b.com ')).toEqual(['a.com', 'b.com'])
  })

  it('空输入给空数组', () => {
    expect(parseDomainsInput('   \n ')).toEqual([])
  })
})
