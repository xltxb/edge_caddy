/**
 * 一个节点为什么没在参与解析，以及**接下来该做什么**。
 *
 * 这一页要回答的是「我配了 40，为什么它没在扛流量」。而**「谁让它退出的」和
 * 「它现在是死是活」是两件事**（CONTEXT.md、ADR-0014）：
 *
 * - `drainedAt` 是**意图**：人明确让它退出服务，回来要走「重新上线」。
 * - `status: down` 是**观察**：主控没收到心跳。
 *
 * 三种原因由后端给（契约 §4 的 `dns_reason`），不由前端推。它们的区别不在
 * 「谁干的」，在**要人做的事完全不同**：手动关的想开就开回来；系统因离线自动
 * 摘的，开解析没用，得先去修那台机器；被下线的要先「重新上线」。
 *
 * 早先这里只凭 `status === 'down'` 就断言「离线，已自动退出解析」，把**人做的
 * 事归给了系统** —— 归错因比不归因贵：不归因的人会去查，归错因的人会照着那个
 * 方向查。后来收成两支（人为下线 / 不归因），那时后端确实只答得出两分；
 * 现在三分都答得出，那句「不归因」才展开。
 */

import type { NodeWire } from '@/api/types'

export type Participation =
  | { kind: 'active' }
  | { kind: 'drained'; text: string; hint: string }
  | { kind: 'auto'; text: string; hint: string }
  | { kind: 'paused'; text: string; hint: string }

export interface DnsWho {
  /** 后端给的原因；空串 = 从没人动过。取不到该节点时传 undefined。 */
  reason?: NodeWire['dns_reason']
  /** 操作人；系统自动摘除时后端给 null，界面不要编一个「system」出来。 */
  actor?: string | null
  offline: boolean
}

/**
 * @param dnsEnabled 该节点在这条线路上的解析开关（来自 `/dns/weights`）
 * @param who        谁关的 —— 来自 `/nodes` 的 dns_reason / dns_actor
 */
export function participation(dnsEnabled: boolean, who: DnsWho): Participation {
  if (dnsEnabled) return { kind: 'active' }

  if (who.reason === 'drained') {
    return {
      kind: 'drained',
      text: '已下线（人为）',
      hint: '权重保留着，但要先「重新上线」才会回到解析里。',
    }
  }

  /*
   * 系统因离线自动摘的：**这一支的重点是「开解析没用」**。
   *
   * 人看到解析关着的第一反应是去开它，而这台机器心跳都没了 —— 开回来只会把
   * 流量送给一台不在的机器。所以这句话要把人推向那台机器，不是推向那个开关。
   */
  if (who.reason === 'auto_offline') {
    return {
      kind: 'auto',
      text: '离线，系统已自动摘除',
      hint: '权重保留着。先去修那台机器 —— 它心跳没了，把解析开回来只会把流量送过去。',
    }
  }

  if (who.reason === 'manual') {
    return {
      kind: 'paused',
      text: who.actor ? `已暂停（${who.actor}）` : '已暂停解析',
      hint: '权重保留着，恢复解析后即可重新分流量。',
    }
  }

  /*
   * 到这里只知道**解析是关的**，不知道是谁关的。
   *
   * 后端也不知道 —— `dns_enabled` 就是个布尔，没记是谁把它关的。所以「人手动
   * 暂停」和「离线自动摘除」这两支，**谁都答不出**，界面就不该假装答得出。
   *
   * 早先这里写过一句「若设置里开了『判定离线后自动退出解析』，就是那条规则
   * 摘的」。那句话有两重问题：它把 `auto_drop_dns` 这个**后端的设置项**搬到了
   * 前端来解释后端的行为（同一份知识存两处，跟服务商的 `covers` 映射一样），
   * 而且它读起来像在归因，只是套了个「若」字。
   *
   * 离线本身是可观察的，写进正文；**为什么被摘的，一个字都不说**。
   */
  return {
    kind: 'paused',
    text: who.offline ? '未参与解析（该节点离线）' : '未参与解析',
    hint: '权重保留着。这里看不出是谁关的解析 —— 恢复解析后即可重新分流量。',
  }
}
