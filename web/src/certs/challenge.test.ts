import { describe, expect, it } from 'vitest'
import { challengeText } from './challenge'
import { certs } from '../../mocks/seed'

describe('证书来源那一列', () => {
  it('imported：说「外部导入」', () => {
    expect(challengeText('imported')).toBe('外部导入')
  })

  /*
   * **「历史」两个字是这条的全部重点。**
   *
   * ADR-0015 之后主控不再签发。把旧的 dns-01 和新导入的显示成同一种东西，
   * 人会以为主控现在还在签，于是不去外部平台续期 —— 而没有任何东西会替他续。
   */
  it('dns-01：必须标出「历史」，否则读起来像主控还在签', () => {
    const t = challengeText('dns-01')
    expect(t).toContain('历史')
    expect(t).not.toBe('外部导入')
  })

  /*
   * 认不出的值**原样显示**，不吞成空白。
   *
   * 库里那一列是自由文本。吞掉的话，一张来路不明的证书看起来跟别的一样 ——
   * 而空白不引起任何疑问，原样显示至少让人看得见有个没见过的值。
   */
  it('认不出的值原样显示，不吞成空白', () => {
    expect(challengeText('内部签发')).toBe('内部签发')
    expect(challengeText('acme-http-01')).toBe('acme-http-01')
    expect(challengeText('')).toBe('')
  })
})

/*
 * 夹具的**完备性**：三种取值都得在，否则界面上对应的那一支走不到。
 *
 * 这一条替换的是原来那句「ACME 签发的证书走 dns-01」——**那条断言的对象没了**
 * （主控不再签发，见 ADR-0015），而顺手把 'dns-01' 改成 'imported' 只会把它
 * 变成一条测夹具等于我自己写的值的空转测试。
 *
 * 「后端到底返回什么值」由 scripts/check-certs.mjs 对真主控验；这里验的是
 * **夹具能不能把界面的每一支都走一遍**。
 */
describe('证书夹具覆盖了三种来源', () => {
  it('imported 与 dns-01 都在 —— 少一种，界面上就有一支永远走不到', () => {
    const kinds = new Set(certs.map((c) => c.challenge))
    expect(kinds.has('imported'), 'seed 里没有 imported —— 那是现在的常态').toBe(true)
    expect(kinds.has('dns-01'), 'seed 里没有 dns-01 —— 「（历史）」那一支走不到').toBe(true)
  })

  it('还有一种映射表认不出的 —— fallback 那一支的夹具', () => {
    const unknown = certs.filter((c) => challengeText(c.challenge) === c.challenge)
    expect(
      unknown.length,
      '夹具里每种取值映射表都认得，于是「原样显示」那一支永远走不到',
    ).toBeGreaterThan(0)
  })
})
