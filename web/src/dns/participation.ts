/**
 * 一个节点为什么没在参与解析。
 *
 * 这一页要回答的是「我配了 40，为什么它没在扛流量」。而**「谁让它退出的」和
 * 「它现在是死是活」是两件事**（CONTEXT.md、ADR-0014）：
 *
 * - `drainedAt` 是**意图**：人明确让它退出服务，回来要走「重新上线」。
 * - `status: down` 是**观察**：主控没收到心跳。它**不蕴含**解析被自动摘掉 ——
 *   那还取决于设置里的 `auto_drop_dns`，关掉的话离线节点的权重照样留着。
 *
 * 早先这里只凭 `status === 'down'` 就断言「离线，已自动退出解析」，于是一台
 * **被人下线之后又离线**的机器会被说成是系统干的 —— **把人做的事归给系统**，
 * 而两者的补救动作完全不同（重新上线 vs 恢复解析）。归错因比不归因贵：
 * 不归因的人会去查，归错因的人会照着那个方向查。
 */

export type Participation =
  | { kind: 'active' }
  | { kind: 'drained'; text: string; hint: string }
  | { kind: 'paused'; text: string; hint: string }

/**
 * @param dnsEnabled 该节点在这条线路上的解析开关（来自 `/dns/weights`）
 * @param drainedAt  人为下线的时刻；取不到该节点时传 undefined
 * @param offline    主控此刻是否判它离线
 */
export function participation(
  dnsEnabled: boolean,
  drainedAt: string | null | undefined,
  offline: boolean,
): Participation {
  if (dnsEnabled) return { kind: 'active' }

  if (drainedAt) {
    return {
      kind: 'drained',
      text: '已下线（人为）',
      hint: '权重保留着，但要先「重新上线」才会回到解析里。',
    }
  }

  /*
   * 到这里只知道**解析是关的**，不知道是谁关的 —— 契约里的 entry 没给原因。
   * 所以只陈述能观察到的：解析关着；顺带说一句它当前是不是离线，
   * 但**不声称两者有因果关系**。
   */
  return {
    kind: 'paused',
    // 离线是**可观察的事实**，写进正文；「所以才被摘的」是**因果**，不写。
    // 只说「未参与解析」的话，一个明明离线的节点看起来跟被人手动关掉的一样，
    // 而那个事实是免费的、就在数据里。
    text: offline ? '未参与解析（该节点离线）' : '未参与解析',
    hint: offline
      ? '权重保留着。该节点当前离线 —— 若设置里开了「判定离线后自动退出解析」，就是那条规则摘的。'
      : '权重保留着，恢复解析后即可重新分流量。',
  }
}
