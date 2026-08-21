import { describe, expect, it } from 'vitest'
import { participation } from './participation'



describe('为什么这个节点没在扛流量 —— 以及该做什么', () => {
  it('解析开着就没什么可解释的', () => {
    expect(participation(true, { offline: false }).kind).toBe('active')
    expect(participation(true, { reason: 'drained', offline: true }).kind).toBe('active')
  })

  /*
   * 三支的区别不在「谁干的」，在**要人做的事完全不同**。所以每一条验的是
   * 「它把人推向哪里」，不是「它说对了名字」。
   */
  it('被下线：推向「重新上线」，不是推向那个开关', () => {
    const p = participation(false, { reason: 'drained', offline: true })
    expect(p.kind).toBe('drained')
    expect(p.kind === 'drained' && p.hint).toContain('重新上线')
  })

  /*
   * 这一支是三支里最要紧的：人看到解析关着的第一反应是去开它，而这台机器
   * 心跳都没了 —— 开回来只会把流量送给一台不在的机器。
   */
  it('系统因离线自动摘的：推向那台机器，并说清开解析没用', () => {
    const p = participation(false, { reason: 'auto_offline', offline: true })
    expect(p.kind).toBe('auto')
    const hint = p.kind === 'auto' ? p.hint : ''
    expect(hint).toContain('先去修那台机器')
    expect(hint).toContain('把解析开回来只会')
  })

  it('人手动关的：带上是谁关的，他心里有数', () => {
    const p = participation(false, { reason: 'manual', actor: 'abiu', offline: false })
    expect(p.kind).toBe('paused')
    expect(p.kind === 'paused' && p.text).toContain('abiu')
    expect(p.kind === 'paused' && p.hint).toContain('恢复解析')
  })

  /*
   * 自动摘除时后端给的 actor 是 null，不是 "system"。界面也不该编一个出来 ——
   * **一个叫 system 的操作人会让人去问那是谁**，而那个账号不存在。
   */
  it('不编造操作人：系统摘的那一支不出现任何名字', () => {
    const p = participation(false, { reason: 'auto_offline', actor: null, offline: true })
    const text = p.kind === 'auto' ? p.text : ''
    expect(text).not.toContain('system')
    expect(text).not.toContain('系统管理员')
  })

  it('manual 但没给操作人：不留一个空括号', () => {
    const p = participation(false, { reason: 'manual', actor: null, offline: false })
    expect(p.kind === 'paused' && p.text).toBe('已暂停解析')
  })

  /*
   * reason 为空串 = 从没人动过；取不到 = /nodes 与 /dns/weights 不同步
   * （那个窗口真实存在，两个接口不是一次查询）。两种都不猜。
   */
  it('从没人动过：只说观察，不归因', () => {
    const p = participation(false, { reason: '', offline: true })
    expect(p.kind).toBe('paused')
    expect(p.kind === 'paused' && p.text).toContain('离线')
    expect(p.kind === 'paused' && p.hint).toContain('看不出是谁关的')
  })

  it('取不到该节点时退回不归因那一支', () => {
    expect(participation(false, { offline: false }).kind).toBe('paused')
  })
})
