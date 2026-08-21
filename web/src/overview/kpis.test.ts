import { describe, expect, it } from 'vitest'
import { buildKpis } from './kpis'
import type { OverviewKpi } from '@/model'

const kpi = (over: Partial<OverviewKpi> = {}): OverviewKpi => ({
  nodesOnline: 5,
  nodesWarn: 0,
  nodesDown: 0,
  nodesTotal: 5,
  connsTotal: 12400,
  connsDeltaPct: 3.2,
  originRate: 8.7,
  driftNodes: 0,
  ...over,
})

const card = (k: OverviewKpi, label: string) => buildKpis(k).find((c) => c.label === label)!

describe('节点在线这一格要和它的脚注对得上账', () => {
  /*
   * 分子只算 status=ok。把 warn 也算进来的话，脚注「异常 N 个」点名的那些节点
   * 会**同时**被算进在线 —— 一个算不平的账比少一个数字更糟：它让人不知道该
   * 信哪一个，而两个都是这一屏上写着的。
   */
  it('在线数 + 异常 + 离线 = 总数', () => {
    const k = kpi({ nodesOnline: 3, nodesWarn: 1, nodesDown: 1, nodesTotal: 5 })
    expect(card(k, '节点在线').value).toBe('3/5')
    expect(k.nodesOnline + k.nodesWarn + k.nodesDown).toBe(k.nodesTotal)
    expect(card(k, '节点在线').foot).toBe('异常 1 个 · 离线 1 个')
  })

  it('全网正常时不硬凑一句「异常 0 个」—— 恒为零的计数会被读成「看过了没问题」', () => {
    expect(card(kpi(), '节点在线').foot).toBe('全网正常')
    expect(card(kpi(), '节点在线').tone).toBe('ok')
  })
})

describe('拿不到的数不能说成暂时拿不到', () => {
  /*
   * 这一组是这个文件存在的理由。
   *
   * 契约把 conns_delta_pct 的 null 描述成「冷启动后的第一天」，而后端扫「写了但
   * 没人读」时发现 traffic_samples 整张表从来没被写过 —— 这个 null 是**永久的**。
   *
   * 一个说自己是暂时的永久状态，比一个明说「没有」的更耗人：它让人明天再来看
   * 一眼，后天再看一眼，每次消耗一点点信任，而且从不触发追查。
   */
  it('没有同比数据时不暗示它会自己好起来', () => {
    const foot = card(kpi({ connsDeltaPct: null }), '全网连接数').foot
    expect(foot).toBe('暂无同比数据')
    for (const w of ['历史不足', '第一天', '稍后', '正在', '积累']) {
      expect(foot, `脚注里出现了「${w}」，那是在承诺它会来`).not.toContain(w)
    }
  })

  it('没有流量数据时同样不暗示样本在积累', () => {
    const c = card(kpi({ originRate: null }), '回源率')
    expect(c.value).toBe('—') // 不是 0：「0% 回源」是一个很强的说法
    expect(c.foot).toBe('暂无流量数据')
    expect(c.foot).not.toContain('还没有')
  })

  it('有数据时照常给出数字 —— 证明上面两条不是因为一律留白才绿的', () => {
    expect(card(kpi(), '全网连接数').foot).toBe('较昨日同时段 +3.2%')
    expect(card(kpi(), '回源率').value).toBe('8.7')
    expect(card(kpi({ connsDeltaPct: -4.5 }), '全网连接数').foot).toContain('-4.5%')
  })
})

describe('每一格的 ⓘ 说的是「这个数字不包含什么」', () => {
  /*
   * 漂移那一格只比对版本号（ADR-0002）。不设这个限的话，一个「0 个节点漂移」
   * 会被读成「所有节点上的配置都是对的」—— 而有人 SSH 上去手改过的，
   * 这个数字一辈子看不见。
   */
  it('漂移那格必须说明它看不见手改', () => {
    const c = card(kpi({ driftNodes: 0 }), '配置漂移')
    expect(c.caveat).toContain('版本号')
    expect(c.caveat).toContain('SSH')
  })

  it('回源率那格必须撇清「不是缓存命中」—— 官方 Caddy 没有缓存模块', () => {
    expect(card(kpi(), '回源率').caveat).toContain('不是缓存命中')
  })
})
