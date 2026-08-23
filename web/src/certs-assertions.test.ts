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
   * **这一条换过判据，不是改了个值。**
   *
   * 原来它守的是「ACME 签发的证书走 dns-01」。ADR-0015 之后主控不再签发 ——
   * **那个被守的行为整个没有了**，而顺手把 'dns-01' 改成 'imported' 会得到
   * 一条看着还在工作、实际上只是在复述我自己刚写进夹具的值的测试。
   *
   * 现在守的是契约 §9 那条：每张受管证书都得说得出自己从哪来，取值只能是
   * `imported`（外部导入）或 `dns-01`（ADR-0015 之前的历史）。冒出第三种值
   * 时界面会原样显示它 —— 那是有意的（见 src/certs/challenge.ts），
   * 但**在受管证书上出现就是后端标错了**。
   *
   * 内部 CA 的客户端证书（回源 mTLS 那张）既不是导入也不是 ACME，不归这条管。
   * 这个排除不是为夹具开的后门：真主控上同样有那张证书。
   */
  it('每张受管证书的来源都在已知取值里；内部 CA 那张不在此列', () => {
    const KNOWN = new Set(['imported', 'dns-01'])
    const managed = certs.filter((c) => !/internal|self-signed/i.test(c.issuer))
    expect(managed.length, '受管证书一张都没有，这条测的是空集').toBeGreaterThan(0)
    for (const c of managed) {
      expect(c.issuer, `${c.domain} 没有 issuer`).toBeTruthy()
      expect(
        KNOWN.has(c.challenge),
        `${c.domain} 的 challenge 是「${c.challenge}」，不在 ${[...KNOWN].join(' / ')} 里`,
      ).toBe(true)
    }
    // 反面对照：内部那张确实存在，证明上面的过滤不是把所有东西都滤掉了
    expect(certs.some((c) => /internal/i.test(c.issuer)), '夹具里没有内部 CA 那张').toBe(true)
  })
})
