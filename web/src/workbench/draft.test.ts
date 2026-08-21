import { describe, expect, it } from 'vitest'
import { applyEdit, changeCount, isFieldDirty, merge, prune, valuesEqual } from './draft'

const route = {
  domain: 'api.example.com',
  upstream: '10.8.0.2:8080',
  body_max: '5MB',
  whitelist: ['1.1.1.1', '2.2.2.2'],
  mtls: false,
}

const rule = {
  id: 'partner',
  enabled: true,
  apply_to: ['api.example.com'],
  spec: { header: 'X-Key', algo: 'hmac-sha256', ttl_s: 300, replay_protection: true },
}

describe('valuesEqual', () => {
  it('字符串数组按去空行规范化后比较', () => {
    expect(valuesEqual(['a', 'b'], [' a ', '', 'b', '  '])).toBe(true)
  })

  it('顺序不同仍算不同 —— 白名单顺序会写进配置', () => {
    expect(valuesEqual(['a', 'b'], ['b', 'a'])).toBe(false)
  })

  it('对象逐键比', () => {
    expect(valuesEqual({ a: 1, b: 2 }, { b: 2, a: 1 })).toBe(true)
    expect(valuesEqual({ a: 1 }, { a: 1, b: 2 })).toBe(false)
  })
})

describe('merge', () => {
  it('顶层字段被草稿覆盖', () => {
    expect(merge(route, { upstream: '10.8.0.9:9000' }).upstream).toBe('10.8.0.9:9000')
  })

  it('spec 再往下合一层 —— 改一个键不该抹掉同 spec 的其他键', () => {
    const eff = merge(rule, { spec: { header: 'X-New' } })
    expect(eff.spec).toEqual({
      header: 'X-New',
      algo: 'hmac-sha256',
      ttl_s: 300,
      replay_protection: true,
    })
  })

  it('没有草稿时原样返回', () => {
    expect(merge(route, undefined)).toBe(route)
  })
})

describe('prune', () => {
  it('剪掉与线上等值的键', () => {
    expect(prune(route, { upstream: '10.8.0.2:8080', body_max: '10MB' })).toEqual({
      body_max: '10MB',
    })
  })

  it('剪空了就整个去掉这个键', () => {
    expect(prune(rule, { spec: { header: 'X-Key' } })).toEqual({})
  })

  it('白名单只多敲了空行 —— 不算改动', () => {
    expect(prune(route, { whitelist: ['1.1.1.1', '', '2.2.2.2', '  '] })).toEqual({})
  })
})

describe('applyEdit', () => {
  it('改一个顶层字段', () => {
    expect(applyEdit(route, {}, 'body_max', '10MB')).toEqual({ body_max: '10MB' })
  })

  it('改回与线上一致时把键删掉 —— 否则蓝点和待下发计数会虚报', () => {
    const once = applyEdit(route, {}, 'body_max', '10MB')
    expect(changeCount(once)).toBe(1)

    const back = applyEdit(route, once, 'body_max', '5MB')
    expect(back).toEqual({})
    expect(changeCount(back)).toBe(0)
  })

  it('改嵌套字段时保住 spec 里的其他键', () => {
    const p = applyEdit(rule, {}, 'spec.header', 'X-New')
    expect(p).toEqual({ spec: { header: 'X-New' } })
    expect(merge(rule, p).spec).toMatchObject({ algo: 'hmac-sha256', ttl_s: 300 })
  })

  it('嵌套字段改回原值也要被剪掉', () => {
    const p1 = applyEdit(rule, {}, 'spec.ttl_s', 600)
    expect(changeCount(p1)).toBe(1)
    const p2 = applyEdit(rule, p1, 'spec.ttl_s', 300)
    expect(p2).toEqual({})
  })

  it('两个字段各改一次，只剪掉改回去的那个', () => {
    let p = applyEdit(rule, {}, 'spec.ttl_s', 600)
    p = applyEdit(rule, p, 'spec.header', 'X-New')
    expect(changeCount(p)).toBe(2)
    p = applyEdit(rule, p, 'spec.ttl_s', 300)
    expect(p).toEqual({ spec: { header: 'X-New' } })
    expect(changeCount(p)).toBe(1)
  })

  it('白名单加一条算一处改动', () => {
    const p = applyEdit(route, {}, 'whitelist', ['1.1.1.1', '2.2.2.2', '3.3.3.3'])
    expect(changeCount(p)).toBe(1)
  })
})

describe('isFieldDirty', () => {
  it('只有真正不同的字段才算脏', () => {
    const p = applyEdit(rule, {}, 'spec.header', 'X-New')
    expect(isFieldDirty(rule, p, 'spec.header')).toBe(true)
    expect(isFieldDirty(rule, p, 'spec.algo')).toBe(false)
    expect(isFieldDirty(rule, undefined, 'spec.header')).toBe(false)
  })
})
