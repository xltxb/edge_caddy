/**
 * 「这张证书覆盖什么」—— 从 `domains` 算出来的一句摘要。
 *
 * ## 它替掉的那个字段是假的
 *
 * 这一格原来渲染 `scope`（「单域名」/「通配符」），而后端 `certResp` 里
 * **从来没有过那个字段** —— 线上那一格一直是空的，只有 mock 的 seed 提供了它，
 * 所以 dev 下一直看着正常。现在 `domains` 由后端**从证书本身读出来**
 * （契约 §9，不落库：存副本意味着两处真相，而两处迟早分叉）。
 *
 * ## 摘要之外，完整列表要留得住
 *
 * 一张 `*.a.com` 的证书**不覆盖** `x.y.a.com`（RFC 6125，通配符只匹配一级）。
 * 所以「通配符」三个字回答不了「我这个域名在不在里面」——那句摘要只够扫，
 * 真要核对得看全量。调用方把 `domains.join()` 放进 `title`，别只留摘要。
 */

/** 摘要。**不吞掉信息量**：多域名时说出个数，含通配符时点出来。 */
export function coverageText(domains: string[] | null | undefined): string {
  const list = domains ?? []
  /*
   * 空的时候说「未知」，不说「单域名」也不留白。
   *
   * 后端从证书本身解析这个字段，解析不出来才会是空 —— 那时**这张证书覆盖
   * 什么是不知道的**，而「单域名」会被读成「只覆盖那一个」，留白会被读成
   * 「没什么特别的」。两种都是在没有信息的地方替人给了个结论。
   */
  if (list.length === 0) return '覆盖范围未知'

  const wild = list.some((d) => d.startsWith('*.'))
  if (list.length === 1) return wild ? '通配符' : '单域名'
  return wild ? `${list.length} 个域名（含通配符）` : `${list.length} 个域名`
}
