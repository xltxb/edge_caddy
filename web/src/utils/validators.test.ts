import { describe, it, expect } from 'vitest'
import { validateRoute } from './validators'

// 这一份校验与后端 internal/api/routes.go 的规则必须一致。
//
// 它存在的理由只有一个：设计稿要求即时反馈（非法输入立刻标红、禁用推送）。
// 代价是同一套规则活在两处、会慢慢漂开——漂开的表现是「表单说 OK 但服务端
// 拒绝」，或者更糟的「表单拒绝了服务端本可接受的值」，后者用户完全无从申诉。
//
// 冒烟测试跑真后端，是这两处漂开时唯一会红的地方。
describe('路由表单校验', () => {
  it('接受合法输入', () => {
    expect(validateRoute({ domain: 'api.example.com', upstream: '10.8.0.2:8080' })).toEqual({})
    expect(validateRoute({ domain: 'a.b.example.com', upstream: 'origin.internal:443' })).toEqual({})
  })

  it('拒绝非法域名', () => {
    for (const domain of ['', 'example', 'http://api.example.com', 'api example.com', '-bad.example.com']) {
      expect(validateRoute({ domain, upstream: '10.0.0.1:80' }).domain,
        `域名 ${JSON.stringify(domain)} 应被拒绝`).toBeTruthy()
    }
  })

  // 回源必须带端口。少了端口 Caddy 会去连 80，而用户以为自己配的是别的端口——
  // 这类错配不会报错，只会把流量默默送错地方。
  it('拒绝缺端口或格式不对的回源地址', () => {
    for (const upstream of ['', '10.0.0.1', 'http://10.0.0.1:80', '10.0.0.1:', ':8080', '10.0.0.1:999999']) {
      expect(validateRoute({ domain: 'a.example.com', upstream }).upstream,
        `回源 ${JSON.stringify(upstream)} 应被拒绝`).toBeTruthy()
    }
  })

  it('拒绝非法的请求体上限', () => {
    expect(validateRoute({ domain: 'a.example.com', upstream: '1.1.1.1:80', body_max: '不是大小' }).body_max).toBeTruthy()
    expect(validateRoute({ domain: 'a.example.com', upstream: '1.1.1.1:80', body_max: '64MiB' }).body_max).toBeFalsy()
  })

  // 白名单逐条报错，而不是笼统说「白名单有问题」——
  // 一屏几十条 IP 时，不指出是哪一条等于没报。
  it('逐条指出非法的 IP / CIDR', () => {
    const errs = validateRoute({
      domain: 'a.example.com', upstream: '1.1.1.1:80',
      wl: ['203.0.113.7', '不是IP', '10.8.0.0/24', '999.1.1.1'],
    })
    expect(errs.wl).toContain('不是IP')
    expect(errs.wl).toContain('999.1.1.1')
    expect(errs.wl).not.toContain('203.0.113.7')
  })

  // 空行与首尾空白应被忽略，不算错误。
  // 用户在文本框里敲回车是常态，把它当成非法输入会很烦人。
  it('忽略空行与首尾空白', () => {
    expect(validateRoute({
      domain: 'a.example.com', upstream: '1.1.1.1:80',
      wl: ['  203.0.113.7  ', '', '   '],
    }).wl).toBeFalsy()
  })
})
