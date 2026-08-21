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
  connsDeltaReason: null,
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

describe('说不出数字时，要说清是哪一种说不出', () => {
  /*
   * 这一组是这个文件存在的理由，而**它的理由变过一次**。
   *
   * 早先响应里只有一个不带原因的 null，那时的不变量是「一律不承诺它会好起来」——
   * 因为三种原因里只有一种会自己好，而我们分不出是哪种，挑一个说会让另两种的人
   * 白等。后端按这条论证补了 conns_delta_reason，于是不变量变了：
   * **只对会自己好的那一种承诺，另外两种照旧不承诺。**
   *
   * 这不是把测试改松了。原来的约束是「在分不清的时候别猜」，现在分得清了，
   * 约束就该落在「分清之后别说错」上。
   */
  it('insufficient_history：这一种可以说「明天就有」', () => {
    const foot = card(kpi({ connsDeltaPct: null, connsDeltaReason: 'insufficient_history' }), '全网连接数').foot
    expect(foot).toContain('明天')
  })

  it('no_sample：不承诺明天有 —— 主控再停一次就又没有了', () => {
    const foot = card(kpi({ connsDeltaPct: null, connsDeltaReason: 'no_sample' }), '全网连接数').foot
    expect(foot).toContain('没有采到样本')
    expect(foot, '这一种不该承诺').not.toContain('明天')
  })

  it('zero_baseline：说清不是故障，是昨天真没连接', () => {
    const foot = card(kpi({ connsDeltaPct: null, connsDeltaReason: 'zero_baseline' }), '全网连接数').foot
    expect(foot).toContain('没有连接')
    expect(foot).not.toContain('明天')
  })

  /*
   * 认不出的取值退回中性那句。
   *
   * 后端加了第四种原因而我没跟上时，最坏的结果应该是「少说一句」，
   * 不该是「把一个不认识的原因说成某个认识的」—— 后者会让人照着一个
   * 根本不适用的建议去等。
   */
  it('认不出的原因退回中性那句，不猜', () => {
    const foot = card(
      kpi({ connsDeltaPct: null, connsDeltaReason: '后端将来新加的' as never }),
      '全网连接数',
    ).foot
    expect(foot).toBe('暂无同比数据')
  })

  it('没有流量数据时不暗示样本在积累', () => {
    const c = card(kpi({ originRate: null }), '回源率')
    expect(c.value).toBe('—') // 不是 0：「0% 回源」是一个很强的说法
    expect(c.foot).toBe('暂无流量数据')
    expect(c.foot).not.toContain('还没有')
  })

  it('有数据时照常给出数字 —— 证明上面几条不是因为一律留白才绿的', () => {
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
