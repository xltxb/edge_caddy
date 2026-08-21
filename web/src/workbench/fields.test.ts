import { describe, expect, it } from 'vitest'
import { LOG_FIELDS, type LogPolicy } from './fields'
import { isVisible, resolveUnavailable } from './field-spec'

const logPolicy = (spec: Partial<LogPolicy['spec']>): LogPolicy => ({
  id: 'log',
  name: '日志与限流',
  version: 0,
  spec: {
    format: 'json',
    level: 'INFO',
    roll_size: 50,
    roll_keep: 5,
    strip_headers: true,
    rate_limit: false,
    ...spec,
  },
})

const field = (path: string) => LOG_FIELDS.find((f) => f.field === path)!

describe('限流：官方 Caddy 没有这个模块', () => {
  it('关着时置灰，原因就地说清', () => {
    const reason = resolveUnavailable(field('spec.rate_limit'), logPolicy({}))
    expect(reason).toContain('限流模块')
    expect(reason).toContain('官方')
  })

  /*
   * 「做不到」不等于「锁死」。库里已经是 true 时（有人直接调过 API），
   * 控件必须还能被操作——否则人被困在一个下发一定会被拒的状态里，
   * 而唯一的出路是去调 API，那等于界面把自己关在门外。
   */
  it('已经是 true 时不置灰，且用错误告诉人这条会让下发被拒', () => {
    const v = logPolicy({ rate_limit: true })
    expect(resolveUnavailable(field('spec.rate_limit'), v)).toBeNull()
    expect(field('spec.rate_limit').validate!(v)).toContain('下发被拒绝')
  })

  it('关着时不渲染 rps / burst —— 免得 diff 里凭空多两行', () => {
    const v = logPolicy({})
    expect(isVisible(field('spec.rate_rps'), v)).toBe(false)
    expect(isVisible(field('spec.rate_burst'), v)).toBe(false)
  })

  it('露面时也是置灰的：改了不会生效', () => {
    const v = logPolicy({ rate_limit: true, rate_rps: 200 })
    expect(isVisible(field('spec.rate_rps'), v)).toBe(true)
    expect(resolveUnavailable(field('spec.rate_rps'), v)).toBeTruthy()
  })
})

/*
 * 与真主控对齐的一条：GET /policies/log 现在回补齐后的默认值（rate_limit=false）。
 * 契约 §6.3 的表里 seed 写的是 true —— 那个值下发一定会被拒。
 * 这条测试钉住我们跟着**实现**走，不跟着那张表走。
 */
describe('默认策略', () => {
  it('默认不开限流', () => {
    expect(
      resolveUnavailable(field('spec.rate_limit'), logPolicy({})),
    ).toBeTruthy()
    expect(field('spec.rate_limit').validate!(logPolicy({}))).toBeNull()
  })
})
