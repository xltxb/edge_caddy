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
 * 这里只测**关掉时**渲染器怎么表现，名字照这个写。
 *
 * 「后端的默认值是什么」是一条**关于世界**的声明，归 scripts/check-premises.mjs
 * 去问真主控 —— 这个文件里的夹具证明不了它。
 *
 * （历史：这一组第一版叫「默认不开限流」，而它断言的是我自己夹具里写的 false。
 * 从名字推改坏立刻露馅：后端哪天把默认改回 true，这条照样绿。）
 */
describe('限流关着时的表现', () => {
  it('置灰且不报错 —— 那是个合法状态，不是待修的问题', () => {
    expect(resolveUnavailable(field('spec.rate_limit'), logPolicy({}))).toBeTruthy()
    expect(field('spec.rate_limit').validate!(logPolicy({}))).toBeNull()
  })
})

/**
 * **这个全局开关做不到，而限流本身现在做得到了 —— 走的是另一条路。**
 *
 * 后端把限流做成了一种访问规则（`rate_limit`），由 Caddy 通过 forward_auth
 * 委托给节点上的 Agent。契约 §6.3 这个全局开关仍然是 1002，两件事不冲突。
 *
 * 但界面上会冲突：人在工作台看到「做不到」，转头在访问控制里建了一条工作正常
 * 的限流规则。那句话没错，而**一句只说了一半的实话，读起来跟假话一样**。
 *
 * 这条钉住那半句。它不验「限流规则能不能用」—— 那是别处的事；
 * 它验的是**这里不会把人拦在一条其实走得通的路前面**。
 */
describe('这个开关做不到，但要指出哪条路走得通', () => {
  it('置灰的理由里要指向访问控制的限流规则', () => {
    const reason = resolveUnavailable(field('spec.rate_limit'), logPolicy({}))!
    expect(reason, '只说了做不到，没说另一条路 —— 人会以为这个系统不能限流').toContain(
      '访问控制',
    )
  })

  it('已经打开时那条错误也要指路，而不只是「请关掉」', () => {
    const f = field('spec.rate_limit')
    const msg = f.validate!(logPolicy({ rate_limit: true }))!
    expect(msg).toContain('访问控制')
  })
})
