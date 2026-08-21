import { describe, expect, it } from 'vitest'
import { participation } from './participation'

const AT = '2026-08-21T16:52:26+08:00'

describe('为什么这个节点没在扛流量', () => {
  it('解析开着就没什么可解释的', () => {
    expect(participation(true, null, false).kind).toBe('active')
    // 一台已下线的机器解析仍开着时也不解释 —— 那是 dns_enabled 说了算
    expect(participation(true, AT, true).kind).toBe('active')
  })

  /*
   * 这一条是这组的理由。
   *
   * 早先只凭 status === 'down' 判「离线，已自动退出解析」，于是一台**被人下线
   * 之后又离线**的机器会被说成是系统干的。两者的补救动作完全不同：
   * 重新上线 vs 恢复解析 —— 归错因的人会照着错的方向查。
   */
  it('人为下线 + 已离线：说人为，不说自动', () => {
    const p = participation(false, AT, true)
    expect(p.kind).toBe('drained')
    expect(p.kind === 'drained' && p.hint).toContain('重新上线')
  })

  it('人为下线但还在线：一样说人为', () => {
    expect(participation(false, AT, false).kind).toBe('drained')
  })

  /*
   * 没下线过的离线节点：**不声称是自动摘的**。
   *
   * 自动摘除还取决于设置里的 auto_drop_dns，关掉的话离线节点的权重照样留着。
   * 所以只陈述观察（当前离线），并把那个条件说出来，不下断言。
   */
  it('没下线过 + 离线：陈述观察，不断言因果', () => {
    const p = participation(false, null, true)
    expect(p.kind).toBe('paused')
    // 「离线」是可观察的事实，进正文；「所以才被摘的」是因果，不进
    expect(p.kind === 'paused' && p.text).toContain('离线')
    const hint = p.kind === 'paused' ? p.hint : ''
    expect(hint).toContain('离线')
    expect(hint).toContain('若') // 条件句，不是断言
    expect(hint).not.toContain('已自动退出解析')
  })

  it('没下线过 + 在线：就是有人手动关的', () => {
    const p = participation(false, null, false)
    expect(p.kind).toBe('paused')
    expect(p.kind === 'paused' && p.hint).toContain('恢复解析')
  })

  // 取不到该节点时（/nodes 与 /dns/weights 不同步）不能因此断言「没被下线过」
  it('drainedAt 取不到时按未知处理，退回不归因的那一支', () => {
    expect(participation(false, undefined, false).kind).toBe('paused')
  })
})
