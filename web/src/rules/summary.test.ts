import { describe, expect, it } from 'vitest'
import { TYPE_LABEL, ruleStatus, ruleSummary } from './summary'
import type { RuleWire } from '@/api/types'

const rule = (over: Partial<RuleWire> = {}): RuleWire => ({
  id: 'r1',
  name: '一条规则',
  type: 'ip_whitelist',
  enabled: true,
  apply_to: ['api.example.com'],
  version: 1,
  spec: {},
  ...over,
})

/**
 * **黑白名单在这一列上必须分得开。**
 *
 * 它们的 spec 一模一样（都只有一个 `ips`），而在渲染出的 Caddy 配置里
 * 只差一个 `not`（契约 §6.2）—— 少写那个 `not` 就把「只拦这些」变成
 * 「只放这些」，**而配置完全合法、站点看起来也正常**（只要访问者恰好在名单里）。
 *
 * 类型列那两个标签只差一个字。摘要要是也一样，这两种相反的规则在列表上
 * 就是同一个外观。
 */
describe('黑白名单的方向', () => {
  const ips = { ips: ['1.1.1.1', '2.2.2.2'] }

  it('白名单说「只放」', () => {
    expect(ruleSummary(rule({ type: 'ip_whitelist', spec: ips }))).toContain('只放')
  })

  it('黑名单说「拦」', () => {
    expect(ruleSummary(rule({ type: 'ip_blacklist', spec: ips }))).toContain('拦')
  })

  /*
   * 这一条才是上面两条真正要说的事：**两句话不能相同**。
   *
   * 分开写是因为「都含『只放』」也能让上面第一条过 —— 而那正是要防的那个 bug
   * （两种相反规则共用一个摘要）。
   */
  it('同样的 spec，两个类型的摘要必须不同', () => {
    const wl = ruleSummary(rule({ type: 'ip_whitelist', spec: ips }))
    const bl = ruleSummary(rule({ type: 'ip_blacklist', spec: ips }))
    expect(wl, '黑白名单的摘要一样 —— 列表上分不出这两条相反的规则').not.toBe(bl)
  })
})

describe('其余几种的要点', () => {
  /*
   * **「命中任一」四个字不能省。** 多条特征之间是「或」（契约 §6.2），
   * 而一个光秃秃的「2 个特征」会被按「且」读 —— 那时人以为自己配的是
   * 「路径是 /.git 且 UA 是 sqlmap」，实际两条各自都会拦。
   */
  it('请求特征：说出多条之间是「或」', () => {
    const s = ruleSummary(
      rule({
        type: 'request_filter',
        spec: { filters: [{ field: 'path' }, { field: 'user_agent' }] },
      }),
    )
    expect(s).toContain('2')
    expect(s, '没说清多条之间是「或」，人会按「且」去读').toContain('任一')
  })

  /*
   * **不写「限速」。** 它是令牌桶：`requests` 同时是持续速率的分子和
   * 允许的突发。「限速」盖住了后一半，而那一半正是它不误伤正常用户的原因。
   *
   * 断言的是两个数都出现 —— 只说「限速 100」的话，`window_s` 就丢了，
   * 而 100 次 / 秒和 100 次 / 分是两件完全不同的事。
   */
  it('限流：两个数都要出现，不写「限速」', () => {
    const s = ruleSummary(
      rule({ type: 'rate_limit', spec: { requests: 100, window_s: 60, rate_key: 'ip' } }),
    )
    expect(s).toContain('100')
    expect(s, 'window_s 丢了 —— 100 次/秒和 100 次/分是两件事').toContain('60')
    expect(s).not.toContain('限速')
  })

  it('限流：ip_path 要跟 ip 分得开', () => {
    const base = { requests: 10, window_s: 1 }
    const byIp = ruleSummary(rule({ type: 'rate_limit', spec: { ...base, rate_key: 'ip' } }))
    const byPath = ruleSummary(
      rule({ type: 'rate_limit', spec: { ...base, rate_key: 'ip_path' } }),
    )
    expect(byIp).not.toBe(byPath)
    expect(byPath).toContain('路径')
  })

  /*
   * **地域也要带方向**，跟黑白名单同一条理由 —— 而这一个更险：
   * 方向是 spec 里的一个字段（`geo_mode`），类型列上两者完全相同。
   * 摘要不带方向的话，「只放中国」和「拦掉中国」在列表上一模一样。
   */
  it('地域：allow 和 block 的摘要必须不同', () => {
    const cs = { geo_countries: ['CN', 'HK'] }
    const allow = ruleSummary(rule({ type: 'geo_block', spec: { ...cs, geo_mode: 'allow' } }))
    const block = ruleSummary(rule({ type: 'geo_block', spec: { ...cs, geo_mode: 'block' } }))
    expect(allow, '两个相反方向的摘要一样 —— 类型列上它们本来就没有区别').not.toBe(block)
    expect(allow).toContain('只放')
    expect(block).toContain('拦')
  })

  it('地域：国家多时截断，但要说出总数', () => {
    const many = ['A1', 'B2', 'C3', 'D4', 'E5', 'F6', 'G7', 'H8']
    const s = ruleSummary(rule({ type: 'geo_block', spec: { geo_mode: 'block', geo_countries: many } }))
    expect(s, '截断了却没说总共有几个').toContain('8')
  })

  it('七种类型都有标签 —— 少一个的话那一列会空着', () => {
    const types: RuleWire['type'][] = [
      'ip_whitelist',
      'ip_blacklist',
      'request_filter',
      'rate_limit',
      'geo_block',
      'service_secret',
      'jwt_bearer',
    ]
    for (const t of types) {
      expect(TYPE_LABEL[t], `${t} 没有标签`).toBeTruthy()
    }
  })
})

/**
 * 状态列。**说的是「这条规则此刻做不做事」，不是那个开关的位置。**
 */
describe('ruleStatus', () => {
  it('停用 / 未绑定域名：都不是「生效中」', () => {
    expect(ruleStatus(rule({ enabled: false }), 0).tone).toBe('warn')
    expect(ruleStatus(rule({ apply_to: [] }), 0).tone).toBe('warn')
  })

  /*
   * **地域规则在缺库的节点上形同虚设**（契约 §6.2）。
   *
   * 后端那条决定是「库还没到时放行，而不是全封」—— 代价说在明处：
   * 那段时间这条规则什么也不拦。契约要求它必须被看见。
   */
  it('地域规则 + 有节点缺库：说出几台，而不是「生效中」', () => {
    const st = ruleStatus(rule({ type: 'geo_block' }), 3)
    expect(st.tone).toBe('warn')
    expect(st.text, '没说出是几台 —— 一台和全部是两种处境').toContain('3')
  })

  it('地域规则 + 节点都齐：生效中', () => {
    expect(ruleStatus(rule({ type: 'geo_block' }), 0).tone).toBe('ok')
  })

  /*
   * **缺库只影响地域规则。**
   *
   * 这一条挡的是「把那个计数写成一个全局横幅」或者漏了 `r.type` 判断：
   * 一台节点缺 GeoIP 库，跟这台节点上的 IP 白名单能不能拦人毫无关系。
   * 那样每条规则都会跟着变黄，而人两天就学会忽略这一列。
   */
  it('缺库不影响别的类型的规则', () => {
    expect(ruleStatus(rule({ type: 'ip_whitelist' }), 3).tone).toBe('ok')
    expect(ruleStatus(rule({ type: 'rate_limit' }), 3).tone).toBe('ok')
  })
})
