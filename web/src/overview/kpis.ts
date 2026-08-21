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
       * null 时**不说「历史不足」** —— 因为我们不知道是三种里的哪一种。
       *
       * 契约 §3 列了三种：历史不足 24 小时（真·暂时）、昨天那一分钟没样本
       * （主控停过，或那一分钟节点报数不齐被跳过）、昨天那一分钟是 0
       * （分母为 0，百分比没有定义）。**响应里只有一个 null，不带原因。**
       *
       * 只有第一种会自己好起来。说「历史不足」等于在三选一里挑了一个，
       * 而挑错的那两次会让人明天再来看一眼、后天再看一眼 —— 一个说自己是暂时的
       * 长期状态，比一个明说「没有」的更耗人：它每次消耗一点点信任，
       * 而且从不触发追查。
       *
       * （这段注释本身修过一次：早先的理由是「traffic_samples 从来没被写过，
       * 这个 null 是永久的」。#25 做完之后那句话失效了，而结论没变 ——
       * **结论对而理由过期，是我们花了好几轮在清的那一类。**）
       */
      foot: k.connsDeltaPct === null ? '暂无同比数据' : `较昨日同时段 ${signed(k.connsDeltaPct)}`,
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

function signed(n: number): string {
  return `${n >= 0 ? '+' : ''}${n.toFixed(1)}%`
}
