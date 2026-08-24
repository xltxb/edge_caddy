import { describe, expect, it } from 'vitest'
import {
  invalidIps,
  isIpv6,
  isBodyMax,
  isDomain,
  isHostPort,
  isIpOrCidr,
  isIpv4,
  isPositiveInt,
  normalizeLines,
} from './validators'

describe('isIpv4', () => {
  it('接受合法地址', () => {
    for (const s of ['0.0.0.0', '203.0.113.7', '255.255.255.255', '10.8.0.0']) {
      expect(isIpv4(s), s).toBe(true)
    }
  })

  it('拒绝超出 255 的段 —— 设计稿那个正则会放过它', () => {
    expect(isIpv4('999.999.999.999')).toBe(false)
    expect(isIpv4('256.0.0.1')).toBe(false)
  })

  it('拒绝前导零 —— 会被按八进制读，含义会变', () => {
    expect(isIpv4('010.0.0.1')).toBe(false)
  })

  it('拒绝段数不对的', () => {
    expect(isIpv4('1.2.3')).toBe(false)
    expect(isIpv4('1.2.3.4.5')).toBe(false)
  })
})

describe('isIpOrCidr', () => {
  it('接受裸地址与合法前缀', () => {
    expect(isIpOrCidr('10.8.0.0/24')).toBe(true)
    expect(isIpOrCidr('0.0.0.0/0')).toBe(true)
    expect(isIpOrCidr('203.0.113.7')).toBe(true)
  })

  it('拒绝越界前缀', () => {
    expect(isIpOrCidr('10.8.0.0/33')).toBe(false)
    expect(isIpOrCidr('10.8.0.0/99')).toBe(false)
  })

  it('拒绝多个斜杠', () => {
    expect(isIpOrCidr('10.8.0.0/24/8')).toBe(false)
  })

  it('拒绝空前缀', () => {
    expect(isIpOrCidr('10.8.0.0/')).toBe(false)
  })
})

describe('normalizeLines / invalidIps', () => {
  it('去空行与首尾空白 —— 等值比较前必须过这一步', () => {
    expect(normalizeLines([' 1.1.1.1 ', '', '  ', '2.2.2.2'])).toEqual(['1.1.1.1', '2.2.2.2'])
  })

  it('undefined 当空数组，不抛', () => {
    expect(normalizeLines(undefined)).toEqual([])
    expect(invalidIps(undefined)).toEqual([])
  })

  it('只报非法的那几行', () => {
    expect(invalidIps(['1.1.1.1', '10.8.0.0/33', 'nope', '2.2.2.2/24'])).toEqual([
      '10.8.0.0/33',
      'nope',
    ])
  })
})

describe('isHostPort', () => {
  it('接受 IP 与主机名', () => {
    expect(isHostPort('10.8.0.2:8080')).toBe(true)
    expect(isHostPort('origin.internal:443')).toBe(true)
    expect(isHostPort('localhost:7788')).toBe(true)
  })

  it('拒绝缺端口或缺主机', () => {
    expect(isHostPort('10.8.0.2')).toBe(false)
    expect(isHostPort('10.8.0.2:')).toBe(false)
    expect(isHostPort(':8080')).toBe(false)
  })

  it('拒绝越界端口', () => {
    expect(isHostPort('a:0')).toBe(false)
    expect(isHostPort('a:65536')).toBe(false)
  })

  it('拒绝带协议前缀 —— 那是常见的手滑', () => {
    expect(isHostPort('http://10.8.0.2:8080')).toBe(false)
  })
})

describe('isDomain', () => {
  it('接受普通域名与通配符', () => {
    expect(isDomain('api.example.com')).toBe(true)
    expect(isDomain('*.example.com')).toBe(true)
  })

  it('拒绝没有点的单标签', () => {
    expect(isDomain('localhost')).toBe(false)
  })

  it('拒绝首尾连字符', () => {
    expect(isDomain('-bad.example.com')).toBe(false)
    expect(isDomain('bad-.example.com')).toBe(false)
  })
})

describe('isBodyMax', () => {
  it('接受设计稿里出现过的写法', () => {
    for (const s of ['5MB', '64MB', '1MB', '256MB', '512KB']) {
      expect(isBodyMax(s), s).toBe(true)
    }
  })

  it('拒绝没有单位的纯数字 —— 它是人类可读字符串，不是字节数', () => {
    expect(isBodyMax('5242880')).toBe(false)
  })

  it('拒绝未知单位', () => {
    expect(isBodyMax('5TB')).toBe(false)
  })
})

describe('isPositiveInt', () => {
  it('接受正整数，字符串也认', () => {
    expect(isPositiveInt(300)).toBe(true)
    expect(isPositiveInt('60')).toBe(true)
  })

  it('拒绝零、负数、小数与空', () => {
    expect(isPositiveInt(0)).toBe(false)
    expect(isPositiveInt(-1)).toBe(false)
    expect(isPositiveInt(1.5)).toBe(false)
    expect(isPositiveInt('')).toBe(false)
  })
})

/**
 * **IPv6 得填得进去。**
 *
 * 这里原先只认 IPv4 —— 而白名单要放行所有人，得同时有 `0.0.0.0/0` 和
 * `::/0`。后者填了会被判非法，人只能删掉它，于是**界面把唯一正确的做法
 * 拦住了**。
 *
 * 它的后果静默且彻底：IPv6 访问者不在名单里 → `not` 成立 → 按处置方式拦下。
 * 若那条路由是 `abort`，被拦的请求不产生响应、不进日志、也不进 `blocked_1h`。
 * 用户在灰度上撞的就是这个。
 */
describe('IP 校验认 IPv6', () => {
  it('放行所有人要的那两条都合法', () => {
    expect(isIpOrCidr('0.0.0.0/0'), 'IPv4 全网').toBe(true)
    expect(isIpOrCidr('::/0'), 'IPv6 全网 —— 填不进去的话人只能把它删掉').toBe(true)
  })

  it('常见的 v6 写法都认', () => {
    for (const x of ['2001:db8::1', '2001:db8::/32', 'fe80::1', '::1', '::ffff:1.2.3.4']) {
      expect(isIpOrCidr(x), `${x} 被判成了非法`).toBe(true)
    }
  })

  /*
   * **前缀长度按族分**：v4 最大 32，v6 最大 128。
   * 共用一个上限的话，`0.0.0.0/64` 会被放过 —— 那种值一路走到 Caddy 才被拒，
   * 而那时它已经混在一次全网下发里了。
   */
  it('前缀长度按族分：v4 到 32，v6 到 128', () => {
    expect(isIpOrCidr('2001:db8::/128')).toBe(true)
    expect(isIpOrCidr('2001:db8::/129'), 'v6 前缀超过 128').toBe(false)
    expect(isIpOrCidr('10.0.0.0/32')).toBe(true)
    expect(isIpOrCidr('10.0.0.0/33'), 'v4 前缀超过 32').toBe(false)
    expect(isIpOrCidr('10.0.0.0/64'), 'v4 用了 v6 的上限').toBe(false)
  })

  it('乱写的还是要拒', () => {
    for (const x of ['999.999.999.999', '2001:db8::zz', 'not-an-ip', '1.2.3.4/', '::/0/0']) {
      expect(isIpOrCidr(x), `${x} 被放过了`).toBe(false)
    }
  })

  it('isIpv6 不把 IPv4 当成 v6', () => {
    expect(isIpv6('10.0.0.1')).toBe(false)
    expect(isIpv6('2001:db8::1')).toBe(true)
  })
})
