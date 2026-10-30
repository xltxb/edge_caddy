import { describe, expect, it } from 'vitest'
import { AGENT_HEARTBEAT_S, maxSecondsUntilOffline } from './offline'

describe('maxSecondsUntilOffline', () => {
  it('默认配置（3 秒 × 5 次）是 18 秒，不是 15', () => {
    // 越线之后的第一次 tick 最坏要多等一个周期——原先的「周期 × 次数」少算了它。
    expect(maxSecondsUntilOffline(3, 5)).toBe(18)
  })

  it('判定周期比节点心跳快时，那条线按节点的心跳间隔算（#82）', () => {
    // 1 秒 × 2 次原先显示「最长 2 秒」，而主控要等心跳先迟到 3 秒才开始数。
    expect(maxSecondsUntilOffline(1, 2)).toBe(AGENT_HEARTBEAT_S + 2)
  })

  it('判定周期比节点心跳慢时，那条线就是判定周期', () => {
    expect(maxSecondsUntilOffline(10, 3)).toBe(40)
  })
})
