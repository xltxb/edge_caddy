/**
 * 节点 Agent 的心跳间隔（秒）。**设置页改不了它**：`heartbeat_interval_s` 是
 * 主控的判定周期，到不了节点（契约 §11，#82）。主控对还没量出间隔的节点
 * 也按这个数判，所以它在这里出现是在复述契约，不是另立一个数。
 */
export const AGENT_HEARTBEAT_S = 3

/**
 * 「节点最长 N 秒后被判定离线」的 N（契约 §11）。
 *
 * 主控先等样本旧过一条线——判定周期与节点心跳间隔取大的——之后每个周期
 * 记一次错过，满阈值判离线。tick 的相位与心跳到达的时刻无关，第一次越线的
 * tick 最坏要多等一个周期，于是最坏是 `线 + 周期 × 次数`。
 *
 * 原先这里是 `周期 × 次数`：默认配置下少说一个周期（15 而实际 18），
 * 周期设得比 3 秒小时差得更多——1 × 2 显示「最长 2 秒」，而主控要等心跳先
 * 迟到 3 秒才开始数。**一个说少了的「最长」，是在替系统许一个它做不到的诺。**
 */
export function maxSecondsUntilOffline(intervalS: number, threshold: number): number {
  return Math.max(intervalS, AGENT_HEARTBEAT_S) + intervalS * threshold
}
