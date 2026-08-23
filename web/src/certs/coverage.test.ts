import { describe, expect, it } from 'vitest'
import { coverageText } from './coverage'

describe('证书覆盖范围那一格', () => {
  it('单域名', () => {
    expect(coverageText(['api.example.com'])).toBe('单域名')
  })

  it('只有一个通配符：说「通配符」', () => {
    expect(coverageText(['*.example.com'])).toBe('通配符')
  })

  it('多域名：说出个数', () => {
    expect(coverageText(['a.com', 'b.com'])).toBe('2 个域名')
  })

  it('多域名含通配符：两件事都要说', () => {
    const t = coverageText(['api.example.com', '*.api.example.com'])
    expect(t).toContain('2')
    expect(t).toContain('通配符')
  })

  /*
   * **空的时候不能替人给结论。**
   *
   * 后端从证书本身解析这个字段，解析不出来才会是空 —— 那时这张证书覆盖什么
   * 是**不知道的**。说「单域名」会被读成「只覆盖那一个」，留白会被读成
   * 「没什么特别的」，两种都是在没有信息的地方给了个结论。
   *
   * 与 reconnects_1h 那条同形：一个假值的危害取决于它引不引起疑问。
   */
  it('空 / 缺失：明说未知，不落进「单域名」也不留白', () => {
    for (const v of [[], null, undefined]) {
      const t = coverageText(v)
      expect(t).toContain('未知')
      expect(t).not.toBe('单域名')
      expect(t).not.toBe('')
    }
  })
})
