import { describe, expect, it } from 'vitest'
import { TYPE_LABEL, ruleSummary } from './summary'
import { fieldsFor } from '@/workbench/fields'
import { readableFor } from '@/workbench/readable'
import type { RuleType, RuleWire } from '@/api/types'

/**
 * **每一种规则类型，每一张分派表都要认得它。**
 *
 * 规则 type 的分派散在 5 个文件 8 张表里（issue #68）。其中
 * `Record<RuleType, …>` 那几张（SKELETON / TYPE_LABEL / NAME_EG / TYPE_HINT）
 * 少一个键编译器会红，而 `fieldsFor` / `readableFor` / `ruleSummary` 是
 * **if 级联**——漏改全都不报错，症状是空白表单和空摘要（那正是 #67）。
 *
 * 这条测试用 TYPE_LABEL 当权威清单：它是 `Record<RuleType, string>`，
 * 加一种类型时编译器逼你改它，于是新类型会自动进入下面这个循环，
 * 而没跟上的那几张表会在这里红。
 *
 * **登记表是上界**（domain.md 同名小节）：清单本身漏了的话这条测试看不见——
 * 而那一半由 TypeScript 的 Record 守着，两者合起来才闭合。
 */
describe('规则类型的分派表都要跟上', () => {
  const types = Object.keys(TYPE_LABEL) as RuleType[]

  it('清单本身不是空的', () => {
    expect(types.length).toBeGreaterThanOrEqual(7)
  })

  for (const t of types) {
    const rule = { id: 'r1', name: 'n', type: t, enabled: true, apply_to: [], spec: {} } as unknown as RuleWire

    it(`${t}：有字段表，不是「还不认识」那张`, () => {
      const specs = fieldsFor('rule:r1', rule)
      expect(specs.length).toBeGreaterThan(0)
      expect(specs.some((s) => s.kind === 'notice')).toBe(false)
    })

    it(`${t}：有可读表示`, () => {
      expect(readableFor('rule:r1', rule).length).toBeGreaterThan(0)
    })

    it(`${t}：有要点摘要`, () => {
      expect(ruleSummary(rule).trim().length).toBeGreaterThan(0)
    })
  }
})
