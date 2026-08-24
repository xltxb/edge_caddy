import { describe, expect, it } from 'vitest'
import {
  GEO_BLOCK_FIELDS,
  IP_BLACKLIST_FIELDS,
  IP_WHITELIST_FIELDS,
  REQUEST_FILTER_FIELDS,
  fieldsFor,
  rateLimitFields,
  type RuleDraft,
} from './fields'
import { resolveHint, type FieldSpec } from './field-spec'

const rule = (type: string, spec: Record<string, unknown>): RuleDraft =>
  ({
    id: 'r1',
    name: '一条规则',
    type,
    enabled: true,
    apply_to: ['api.example.com'],
    version: 1,
    spec,
  }) as unknown as RuleDraft

const at = (specs: FieldSpec<RuleDraft>[], path: string) => specs.find((f) => f.field === path)!
const hintOf = (specs: FieldSpec<RuleDraft>[], path: string, v: RuleDraft) =>
  resolveHint(at(specs, path), v)
const errOf = (specs: FieldSpec<RuleDraft>[], path: string, v: RuleDraft) => {
  const f = at(specs, path)
  return f.validate ? f.validate(v) : null
}

/**
 * **`fieldsFor` 的兜底曾经是 `IP_WHITELIST_FIELDS`。**
 *
 * 那在只有三种类型时是个无害的默认。加进四种新类型的那一刻它变成一个安静的错，
 * 而最险的一档是 `ip_blacklist`：它的 spec 跟白名单一模一样（都只有一个 `ips`），
 * 于是那张表**正常工作** —— 输入框在、校验在、保存也对，
 * 而每一句文案都在说「允许的来源」，**人正在编辑的是一条拦截规则**。
 *
 * 这一组测的就是那个兜底不能回来。
 */
describe('fieldsFor 的分派', () => {
  it('七种类型各拿到自己的表', () => {
    const pairs: [string, FieldSpec<never>[]][] = [
      ['ip_whitelist', IP_WHITELIST_FIELDS as FieldSpec<never>[]],
      ['ip_blacklist', IP_BLACKLIST_FIELDS as FieldSpec<never>[]],
      ['request_filter', REQUEST_FILTER_FIELDS as FieldSpec<never>[]],
      ['geo_block', GEO_BLOCK_FIELDS as FieldSpec<never>[]],
    ]
    for (const [type, expected] of pairs) {
      expect(fieldsFor('rule:r1', rule(type, {})), `${type} 拿到的不是自己的表`).toBe(expected)
    }
  })

  /*
   * 这一条单独写，因为它是那个 bug 的确切形状：**两个类型拿到同一张表**。
   * 上面那条要是只比「不是空的」，这个 bug 照样能过。
   */
  it('黑名单和白名单不能拿到同一张表', () => {
    const wl = fieldsFor('rule:r1', rule('ip_whitelist', {}))
    const bl = fieldsFor('rule:r1', rule('ip_blacklist', {}))
    expect(bl, '黑名单拿到了白名单那张表 —— 所有文案都会说「允许」').not.toBe(wl)
  })

  /*
   * **认不出的类型返回空表，不返回某张碰巧存在的表。**
   *
   * 一张空表在界面上是看得见的（工作台会说这个类型还没有编辑器），
   * 而一张错的表看起来跟对的一样。
   */
  it('认不出的类型返回空表', () => {
    expect(fieldsFor('rule:r1', rule('something_new', {}))).toHaveLength(0)
  })
})

/**
 * **空名单两者都拒，而理由相反**（契约 §6.2）。
 *
 * 空白名单拦下所有人 —— 一次事故，人会立刻发现。
 * 空黑名单谁也拦不到 —— 一条静默失效的规则，没有任何症状。
 *
 * 两者在列表上长得一模一样：一条启用着的规则。所以两句提示词不能一样。
 */
describe('黑白名单的空名单', () => {
  it('空黑名单：说的是「谁也拦不到」，不是「拦下所有人」', () => {
    const msg = errOf(IP_BLACKLIST_FIELDS, 'spec.ips', rule('ip_blacklist', { ips: [] }))
    expect(msg, '空黑名单没被拦下').toBeTruthy()
    expect(msg, '空黑名单的失效方式是「谁也拦不到」，提示词要说这件事').toContain('拦不到')
  })

  it('黑名单的字段说明说「拦」，白名单说「非白名单流量」—— 两张表的文案不同', () => {
    const ips = { ips: ['1.1.1.1'] }
    const bl = hintOf(IP_BLACKLIST_FIELDS, 'spec.ips', rule('ip_blacklist', ips))
    const wl = hintOf(IP_WHITELIST_FIELDS, 'spec.ips', rule('ip_whitelist', ips))
    expect(bl, '两张表的字段说明一样 —— 那正是兜底 bug 的症状').not.toBe(wl)
  })

  it('黑名单的标签说「拦截」，不说「允许」', () => {
    expect(at(IP_BLACKLIST_FIELDS, 'spec.ips').label).toContain('拦截')
    expect(at(IP_BLACKLIST_FIELDS, 'spec.ips').label).not.toContain('允许')
  })
})

describe('请求特征', () => {
  /*
   * **多条之间是「或」**，而人会按「且」去读一个光秃秃的数字。
   * 这句话要在配第二条时就出现，不是配完四条之后。
   */
  it('两条以上时说出「命中任一」', () => {
    const v = rule('request_filter', {
      filters: [
        { field: 'path', op: 'prefix', value: '/a' },
        { field: 'user_agent', op: 'contains', value: 'x' },
      ],
    })
    expect(hintOf(REQUEST_FILTER_FIELDS, 'spec.filters', v)).toContain('任一')
  })

  it('一条特征都没有：拦下来，理由是「谁也拦不到」', () => {
    const msg = errOf(REQUEST_FILTER_FIELDS, 'spec.filters', rule('request_filter', { filters: [] }))
    expect(msg).toContain('拦不到')
  })

  it('某一条没填值：说出是第几条', () => {
    const v = rule('request_filter', {
      filters: [
        { field: 'path', op: 'prefix', value: '/a' },
        { field: 'path', op: 'prefix', value: '' },
      ],
    })
    expect(errOf(REQUEST_FILTER_FIELDS, 'spec.filters', v), '没说是第几条').toContain('2')
  })
})

describe('限流', () => {
  const spec = { requests: 100, window_s: 60, rate_key: 'ip' }

  /**
   * **「三台节点、每台限 100，全局实际是 300」要在编辑那一刻说出来。**
   *
   * 一个人按「我要限 100」去配，拿到的是 300 —— 而没有任何地方会告诉他。
   * 这不是能修的（全局限流要共享计数器，那个跨机往返比它要防的攻击更容易
   * 先把节点拖垮），所以只能说清楚。
   */
  it('说出全局倍数 —— 3 个节点、每台 100，要出现 300', () => {
    const h = hintOf(rateLimitFields(3), 'spec.requests', rule('rate_limit', spec))
    expect(h, '没说出全局是多少 —— 人按 100 配，拿到的是 300').toContain('300')
    expect(h).toContain('3')
  })

  /*
   * **节点数为 0 时不说那句话。**
   *
   * 0 的意思是「节点列表还没加载」，而「全局约 0 次」是一句假话 ——
   * 它比不说更糟：不说只是少一句，说错会让人以为限流没生效。
   */
  it('节点数为 0（还不知道）时不说全局倍数', () => {
    const h = hintOf(rateLimitFields(0), 'spec.requests', rule('rate_limit', spec))
    expect(h, '节点数还不知道，却说了一个全局数字').not.toContain('全局')
  })

  it('补满周期算得出持续速率', () => {
    const h = hintOf(rateLimitFields(1), 'spec.window_s', rule('rate_limit', spec))
    expect(h, '没算出持续速率').toContain('1.7')
  })

  /*
   * **`ip_path` 只在另配了限定路径的规则时才有意义**（契约 §6.2）：
   * 路径是攻击者能控制的，他换个路径就是一个新桶。
   * 而这个选项本身看起来只是「更精细一点」。
   */
  it('ip_path 要提醒路径是攻击者能控的', () => {
    const h = hintOf(
      rateLimitFields(1),
      'spec.rate_key',
      rule('rate_limit', { ...spec, rate_key: 'ip_path' }),
    )
    expect(h, 'ip_path 没提醒它的前提').toContain('攻击者')
  })

  it('两种分桶方式的说明不同', () => {
    const byIp = hintOf(rateLimitFields(1), 'spec.rate_key', rule('rate_limit', spec))
    const byPath = hintOf(
      rateLimitFields(1),
      'spec.rate_key',
      rule('rate_limit', { ...spec, rate_key: 'ip_path' }),
    )
    expect(byIp).not.toBe(byPath)
  })
})

describe('地域', () => {
  /**
   * **小写会被后端拒，而它的失败方式是静默的**：mmdb 里存的是大写，
   * 配 `cn` 匹配不到任何东西 —— 一条看起来配好了、而什么也不做的规则。
   */
  it('小写国家码被拦下，并说清为什么', () => {
    const msg = errOf(
      GEO_BLOCK_FIELDS,
      'spec.geo_countries',
      rule('geo_block', { geo_mode: 'block', geo_countries: ['cn', 'US'] }),
    )
    expect(msg, '小写没被拦').toBeTruthy()
    expect(msg, '没说清小写的后果').toContain('匹配不到')
  })

  it('写全称也被拦', () => {
    expect(
      errOf(
        GEO_BLOCK_FIELDS,
        'spec.geo_countries',
        rule('geo_block', { geo_mode: 'block', geo_countries: ['China'] }),
      ),
    ).toBeTruthy()
  })

  it('两位大写通过', () => {
    expect(
      errOf(
        GEO_BLOCK_FIELDS,
        'spec.geo_countries',
        rule('geo_block', { geo_mode: 'allow', geo_countries: ['CN', 'HK', 'MO'] }),
      ),
    ).toBeNull()
  })

  /**
   * **「查不到国家」在两个方向上行为相反**（契约 §6.2）：
   * block 放行，allow 拦。
   *
   * 内网地址、保留段在库里查不到国家。一个「只放行中国」的规则不能因为查不到
   * 就把人放进来 —— 这一档是这个选择器真正的分量，而它在两个选项的名字上
   * 完全看不出来。所以两个方向的说明必须不同，且都要提到这一档。
   */
  it('两个方向都说出「查不到国家」时怎么办，而且说的相反', () => {
    const a = hintOf(
      GEO_BLOCK_FIELDS,
      'spec.geo_mode',
      rule('geo_block', { geo_mode: 'allow', geo_countries: [] }),
    )
    const b = hintOf(
      GEO_BLOCK_FIELDS,
      'spec.geo_mode',
      rule('geo_block', { geo_mode: 'block', geo_countries: [] }),
    )
    expect(a, 'allow 没说查不到国家时怎么办').toContain('查不到')
    expect(b, 'block 没说查不到国家时怎么办').toContain('查不到')
    expect(a, '两个方向的说明一样 —— 而它们的行为是相反的').not.toBe(b)
  })

  it('一个国家都没填：拦下来', () => {
    expect(
      errOf(
        GEO_BLOCK_FIELDS,
        'spec.geo_countries',
        rule('geo_block', { geo_mode: 'block', geo_countries: [] }),
      ),
    ).toBeTruthy()
  })
})
