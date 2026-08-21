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
   * 没下线过的离线节点：**一个字都不归因**。
   *
   * 「人手动暂停」和「离线自动摘除」这两支后端也分不出来（dns_enabled 就是个
   * 布尔，没记是谁关的）。谁都答不出的问题，界面不该假装答得出。
   *
   * 也不提 auto_drop_dns —— 那是后端的设置项，在前端拼一句解释后端行为的话，
   * 就是同一份知识存两处；而且套个「若」字的归因仍然是归因。
   */
  it('没下线过 + 离线：只说观察，不提原因也不提后端的设置项', () => {
    const p = participation(false, null, true)
    expect(p.kind).toBe('paused')
    // 「离线」是可观察的事实，进正文
    expect(p.kind === 'paused' && p.text).toContain('离线')
    const hint = p.kind === 'paused' ? p.hint : ''
    expect(hint).not.toContain('自动')
    expect(hint).not.toContain('设置')
    expect(hint).not.toContain('若')
  })

  /*
   * 这条第一版叫「**就是有人手动关的**」—— 而那是错的，`rejoin` 之后正好是这个
   * 状态：下线标记清了、解析仍关着、节点在线。那条路径是我自己建的。
   *
   * 函数本身没归因，只有测试名归了。**测试名是唯一一个不会被执行的部分**：
   * 断言会被跑、会红、会被改坏验证，名字不会。
   */
  it('没下线过 + 在线：仍然不归因（rejoin 之后正是这个状态）', () => {
    const p = participation(false, null, false)
    expect(p.kind).toBe('paused')
    expect(p.kind === 'paused' && p.text).toBe('未参与解析')
    expect(p.kind === 'paused' && p.hint).toContain('恢复解析')
    // 不能说「有人手动关的」—— rejoin 会留下同样的状态
    expect(p.kind === 'paused' && p.hint).not.toContain('手动')
  })

  // 取不到该节点时（/nodes 与 /dns/weights 不同步）不能因此断言「没被下线过」
  it('drainedAt 取不到时按未知处理，退回不归因的那一支', () => {
    expect(participation(false, undefined, false).kind).toBe('paused')
  })
})
