import { describe, expect, it } from 'vitest'
import { fieldsFor } from './fields'
import { readableFor } from './readable'

/**
 * **控制台不认识的东西，要说「不认识」，不能装作认识。**
 *
 * 两处分派都会静默走错（issue #67）：
 *
 *   - 规则：不在 if 级联里的类型返回空表 → 表单区一片空白**且没有任何说明**。
 *     fields.ts 的注释说「工作台会显示『这个类型还没有编辑器』」——
 *     全仓库搜不到那句文案，它只活在注释里。
 *   - 全局策略：`id === 'tls' ? TLS_FIELDS : LOG_FIELDS`，于是**任何**非 tls 的
 *     id 都落进日志策略的表单。这一条更贵：人会在一张不属于这个资源的表单上
 *     改字段、存进草稿、下发出去。
 *
 * 触发条件是后端加一种规则类型或第三种全局策略——而那是迟早的事，
 * 主控与控制台本来就会各自升级。
 */
describe('控制台不认识的类型', () => {
  it('不认识的规则类型不能静默给出一张空表', () => {
    const got = fieldsFor('rule:whatever', { type: 'quantum_shield' })
    expect(got).not.toEqual([])
    // 说明性的那一条要能被人看见：它得带上那个陌生的类型名，
    // 否则人不知道该去升级什么。
    expect(JSON.stringify(got)).toContain('quantum_shield')
  })

  it('不认识的全局策略 id 不能落进日志策略的表单', () => {
    const log = fieldsFor('global:log', { format: 'json' })
    const unknown = fieldsFor('global:quantum', {})
    expect(unknown).not.toEqual(log)
    expect(JSON.stringify(unknown)).toContain('quantum')
  })

  it('已知的那些一个都不能被带坏', () => {
    expect(fieldsFor('global:tls', {}).length).toBeGreaterThan(0)
    expect(fieldsFor('global:log', {}).length).toBeGreaterThan(0)
    expect(fieldsFor('rule:a', { type: 'ip_whitelist' }).length).toBeGreaterThan(0)
    expect(fieldsFor('route:a.example.com', {}).length).toBeGreaterThan(0)
  })

  it('可读表示也要说「不认识」，而不是渲染成一份像模像样的日志策略', () => {
    const text = readableFor('global:quantum', { whatever: 1 })
    expect(text).toContain('quantum')
  })
})
