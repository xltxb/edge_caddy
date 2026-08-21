import { describe, expect, it } from 'vitest'
import { certs } from '../mocks/seed'

/*
 * `scripts/check-certs.mjs` 里那几条断言的**逻辑**，拿 seed 的证书夹具走一遍。
 *
 * 为什么需要这个：那个脚本写完那天，**它的主体一条都没执行过** —— 真主控还没配
 * DNS 服务商，前置检查直接把它挡在门外。一个从没跑过的检查，和一句「凭据到了
 * 我跑一遍」的承诺没有区别，而后者正是我在消息里说了很多轮的东西。
 *
 * 这里跑的是同一段判断（两列真相对不对得上账、ACME 签的是不是走 dns-01），
 * 只是数据来自夹具。凭据配上之后那个脚本对着真主控再跑一遍 —— 那时验的是
 * 「真实数据满不满足」，这里验的是「这几条判断本身写对没有」。
 */
describe('证书断言的逻辑（用夹具走一遍）', () => {
  it('夹具里有证书 —— 否则下面几条测的是空集', () => {
    expect(certs.length).toBeGreaterThan(0)
  })

  it('两列真相对得上账：差额必须等于 missing_nodes 的条数', () => {
    for (const c of certs) {
      expect(typeof c.expected_nodes, `${c.domain}`).toBe('number')
      expect(typeof c.loaded_nodes, `${c.domain}`).toBe('number')
      expect(c.loaded_nodes, `${c.domain} 回执多于账面，这个方向不该发生`).toBeLessThanOrEqual(
        c.expected_nodes,
      )
      expect(Array.isArray(c.missing_nodes), `${c.domain}`).toBe(true)
      expect(
        c.missing_nodes.length,
        `${c.domain} 差额 ${c.expected_nodes - c.loaded_nodes} 台，而 missing_nodes 列了 ` +
          `${c.missing_nodes.length} 台 —— 界面上会同时显示两个版本的真相`,
      ).toBe(c.expected_nodes - c.loaded_nodes)
    }
  })

  /*
   * 内部 CA 的客户端证书**不走 ACME**，所以不该被这条断言管。
   *
   * 我第一版忘了排除它，脚本对着夹具立刻红 —— 而那正是它该红的样子：
   * 断言写窄了，把一个合法状态当成了故障。真主控上同样有这张证书（回源 mTLS
   * 用的那张），所以这个排除不是为夹具开的后门。
   */
  it('ACME 签发的证书走 dns-01；内部 CA 的客户端证书不在此列', () => {
    const acme = certs.filter((c) => !/internal|self-signed/i.test(c.issuer))
    expect(acme.length, 'ACME 证书一张都没有，这条测的是空集').toBeGreaterThan(0)
    for (const c of acme) {
      expect(c.issuer, `${c.domain} 没有 issuer`).toBeTruthy()
      expect(c.challenge, `${c.domain}`).toBe('dns-01')
    }
    // 反面对照：内部那张确实存在，证明上面的过滤不是把所有东西都滤掉了
    expect(certs.some((c) => /internal/i.test(c.issuer)), '夹具里没有内部 CA 那张').toBe(true)
  })
})
