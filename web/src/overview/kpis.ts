/**
 * 总览那四格 —— 抽出来是为了**能被证伪**，不是为了复用（只有一处用）。
 *
 * 这是人打开控制台看到的第一屏，四格里每一格都是一句关于全网的断言。
 * 它此前一个可测的落点都没有，全部逻辑写在模板附近的 computed 里，
 * 只在有人盯着截图看的时候被检查过。
 */

import type { OverviewKpi } from '@/model'

export type KpiFilter = 'all' | 'drift'

export interface KpiCard {
  key: KpiFilter
  label: string
  value: string
  unit: string
  foot: string
  tone: 'ok' | 'warn' | 'muted' | 'faint'
  /** ⓘ 里的话：这个数字**不**包含什么。空串表示没有需要设限的地方。 */
  caveat: string
}

export function buildKpis(k: OverviewKpi): KpiCard[] {
  const trouble = [
    k.nodesWarn ? `异常 ${k.nodesWarn} 个` : '',
    k.nodesDown ? `离线 ${k.nodesDown} 个` : '',
  ]
    .filter(Boolean)
    .join(' · ')

  return [
    {
      key: 'all',
      label: '节点在线',
      // 只有 status=ok 才算在线。把 warn 也算进来的话，脚注「异常 N 个 · 离线 M 个」
      // 与这个分子对不上账 —— 异常节点会同时被算成在线又被点名为异常。
      value: `${k.nodesOnline}/${k.nodesTotal}`,
      unit: '',
      foot: trouble || '全网正常',
      tone: trouble ? 'warn' : 'ok',
      caveat: '',
    },
    {
      key: 'all',
      label: '全网连接数',
      value: (k.connsTotal / 1000).toFixed(1),
      unit: 'k',
      /*
       * null 时说清是**哪一种** null。
       *
       * 这个能力是后端按我的论证补的：早先响应里只有一个不带原因的 null，
       * 界面只能说一句谁都不得罪的「暂无同比数据」—— 挑其中一种说，
       * 会让另外两种的人白等。
       *
       * 现在三种各说各的，因为它们要人做的事完全不同：
       *   - insufficient_history：**再等等就有**（唯一会自己好的一种）
       *   - no_sample：等也未必有，除非主控别再停
       *   - zero_baseline：不是故障，昨天那会儿真没连接
       *
       * 认不出的取值退回中性那句 —— 后端加了新枚举而我没跟上时，
       * 宁可少说一句，也不要把一个不认识的原因说成某个认识的。
       */
      foot: k.connsDeltaPct === null ? deltaFoot(k.connsDeltaReason) : `较昨日同时段 ${signed(k.connsDeltaPct)}`,
      tone: k.connsDeltaPct === null ? 'faint' : k.connsDeltaPct >= 0 ? 'ok' : 'muted',
      caveat: '',
    },
    {
      key: 'all',
      label: '回源率',
      // null 不要当成 0 ——「0% 回源」是一个很强的说法，意味着边缘挡下了全部请求，
      // 而真相是我们还不知道。同上：不说「还没有流量样本」，那也在暗示样本在积累。
      value: k.originRate === null ? '—' : k.originRate.toFixed(1),
      unit: k.originRate === null ? '' : '%',
      // 越低越好：没到达源站的那部分是被访问规则拦下的，**不是缓存命中**
      // —— 官方 Caddy 没有 HTTP 缓存模块（ADR-0001 / ADR-0003 的前提）
      foot:
        k.originRate === null ? '暂无流量数据' : `边缘拦截 ${(100 - k.originRate).toFixed(1)}% 请求`,
      tone: k.originRate !== null && k.originRate > 30 ? 'warn' : 'muted',
      caveat:
        '到达源站的请求占比。剩下的是被访问规则拦下（静默断连 / 403 / 404）或由静态响应处理掉的，不是缓存命中——边缘跑的官方 Caddy 没有缓存模块。',
    },
    {
      key: 'drift',
      label: '配置漂移',
      value: String(k.driftNodes),
      unit: '个节点',
      // ADR-0002：这个 KPI 回答的是「这次下发到没到」，脚注就该直说
      foot: k.driftNodes
        ? `${k.driftNodes} 个节点未收到最近一次下发，点击筛选`
        : '最近一次下发全部到达',
      tone: k.driftNodes ? 'warn' : 'muted',
      caveat:
        '只比对节点上报的版本号，不检查节点上的实际配置。有人 SSH 上去手改过的配置不会在这里显示。',
    },
  ]
}

function deltaFoot(reason: OverviewKpi['connsDeltaReason']): string {
  switch (reason) {
    case 'insufficient_history':
      return '还不满 24 小时，明天这时候就有同比了'
    case 'no_sample':
      return '昨天这一分钟没有采到样本，暂无同比'
    case 'zero_baseline':
      return '昨天这时候没有连接，同比无从算起'
    default:
      return '暂无同比数据'
  }
}

function signed(n: number): string {
  return `${n >= 0 ? '+' : ''}${n.toFixed(1)}%`
}
