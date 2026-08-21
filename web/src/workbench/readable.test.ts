import { describe, expect, it } from 'vitest'
import { policyReadable, readableFor, routeReadable, ruleReadable } from './readable'
import type { PolicyWire, RouteWire, RuleWire } from '@/api/types'

/*
 * 这些测试**不**断言「前端表示 == 后端渲染」。ADR-0007 明确说过那是错的前提：
 * 两份渲染器不要求逐字节一致。这里测的是三类性质：
 *   1. 结构性质（0.0.0.0/0 不该渲染出永远不匹配的拦截段）
 *   2. 条件字段（关掉限流不该凭空多出两行 diff）
 *   3. **ADR 守卫** —— 不要出现那些会教错人的字段名
 */

const route = (over: Partial<RouteWire> = {}): RouteWire => ({
  domain: 'api.example.com',
  upstream: '10.8.0.2:8080',
  block_mode: 'abort',
  mtls: false,
  compress: true,
  body_max: '5MB',
  whitelist: ['203.0.113.7'],
  version: 7,
  ...over,
})

const json = (v: unknown) => JSON.stringify(v)

describe('routeReadable', () => {
  it('白名单为 0.0.0.0/0 时不渲染拦截段 —— 那段永远不匹配', () => {
    const open = json(routeReadable(route({ whitelist: ['0.0.0.0/0'] })))
    expect(open).not.toContain('remote_ip')
    const closed = json(routeReadable(route({ whitelist: ['203.0.113.7'] })))
    expect(closed).toContain('remote_ip')
  })

  it('白名单为空时同样不渲染拦截段', () => {
    expect(json(routeReadable(route({ whitelist: [] })))).not.toContain('remote_ip')
  })

  it('处置方式 abort 与 403 渲染成不同的 static_response', () => {
    expect(json(routeReadable(route({ block_mode: 'abort' })))).toContain('"abort":true')
    const s403 = json(routeReadable(route({ block_mode: '403' })))
    expect(s403).toContain('"status_code":403')
    expect(s403).not.toContain('"abort":true')
  })

  it('关闭压缩时不出现 encode handler', () => {
    expect(json(routeReadable(route({ compress: false })))).not.toContain('encode')
  })

  it('body_max 原样显示人类可读字符串，前端不做单位换算', () => {
    expect(json(routeReadable(route({ body_max: '64MB' })))).toContain('"max_size":"64MB"')
  })

  describe('回源 mTLS', () => {
    it('关闭时 transport 下没有 tls', () => {
      expect(json(routeReadable(route({ mtls: false })))).not.toContain('"tls"')
    })

    it('开启时出现回源客户端证书', () => {
      expect(json(routeReadable(route({ mtls: true })))).toContain('edge-mtls')
    })

    it('不使用 client_certificate_automate —— ADR-0008 否掉了它', () => {
      // 那个字段意味着由节点本机的 Caddy pki app 签发客户端证书，
      // 要求每台节点持有 CA 私钥，且 6 台节点会各自成为独立的 CA
      expect(json(routeReadable(route({ mtls: true })))).not.toContain(
        'client_certificate_automate',
      )
    })
  })
})

describe('ruleReadable', () => {
  const rule = (type: RuleWire['type'], spec: Record<string, unknown>): RuleWire => ({
    id: 'r1',
    name: 'r',
    type,
    enabled: true,
    apply_to: ['api.example.com'],
    version: 1,
    spec,
  })

  it('IP 白名单渲染成命名匹配器 + 静默断连', () => {
    const s = json(ruleReadable(rule('ip_whitelist', { ips: ['1.1.1.1', '', '2.2.2.2'] })))
    expect(s).toContain('"@r1"')
    expect(s).toContain('"ranges":["1.1.1.1","2.2.2.2"]') // 空行被规范化掉
    expect(s).toContain('"abort":true')
  })

  it('JWT 不渲染成 jwtauth —— 那是我们不装的插件（ADR-0003）', () => {
    const s = json(
      ruleReadable(rule('jwt_bearer', { iss: 'https://idp/', aud: 'edge', jwks_url: 'u', skew_s: 60 })),
    )
    expect(s).not.toContain('jwtauth')
    // 真实机制是 Caddy forward_auth 委托给 Agent 的校验端点
    expect(s).toContain('/verify/jwt_bearer/r1')
  })

  it('服务密钥同样走校验端点，不自造 signature 段', () => {
    const s = json(
      ruleReadable(
        rule('service_secret', { header: 'X-Key', algo: 'hmac-sha256', ttl_s: 300, replay_protection: true }),
      ),
    )
    expect(s).toContain('/verify/service_secret/r1')
    expect(s).toContain('verify_endpoint')
  })

  it('未绑定域名的规则 applied_to 是空数组，不假装全局生效', () => {
    const r = rule('ip_whitelist', { ips: [] })
    r.apply_to = []
    expect(json(ruleReadable(r))).toContain('"applied_to":[]')
  })
})

describe('policyReadable', () => {
  const tls = (over: Record<string, unknown> = {}): PolicyWire => ({
    id: 'tls',
    name: 'TLS',
    version: 1,
    spec: {
      ca: 'letsencrypt',
      email: 'ops@example.com',
      key_type: 'p256',
      min_version: '1.2',
      http3: true,
      hsts: true,
      hsts_max_age: 63072000,
      ocsp: false,
      ...over,
    },
  })

  const log = (over: Record<string, unknown> = {}): PolicyWire => ({
    id: 'log',
    name: 'log',
    version: 1,
    spec: {
      format: 'json',
      level: 'INFO',
      roll_size: 50,
      roll_keep: 5,
      strip_headers: true,
      rate_limit: true,
      rate_rps: 200,
      rate_burst: 400,
      ...over,
    },
  })

  it('签发参数单独成段 —— 它不下发给节点（ADR-0001）', () => {
    const s = json(policyReadable(tls()))
    expect(s).toContain('master_issuance')
    expect(s).toContain('"challenge":"dns-01"')
  })

  it('HTTP/3 开关改变 protocols', () => {
    expect(json(policyReadable(tls({ http3: true })))).toContain('"h3"')
    expect(json(policyReadable(tls({ http3: false })))).not.toContain('"h3"')
  })

  it('关闭 HSTS 时不下发该响应头', () => {
    expect(json(policyReadable(tls({ hsts: false })))).not.toContain('Strict-Transport-Security')
  })

  it('关闭限流时不渲染 rate_limit —— 填默认值会让 diff 凭空多两行', () => {
    const off = json(policyReadable(log({ rate_limit: false })))
    expect(off).not.toContain('rate_limit')
    expect(json(policyReadable(log({ rate_limit: true })))).toContain('rate_limit')
  })
})

describe('readableFor', () => {
  it('按 res_key 前缀挑渲染器，输出可直接切行的格式化文本', () => {
    const text = readableFor('route:api.example.com', route())
    expect(text.split('\n').length).toBeGreaterThan(5)
    expect(text).toContain('api.example.com')
  })
})
