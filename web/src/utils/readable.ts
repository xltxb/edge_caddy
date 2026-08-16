import type { Route } from '@/api/types'
import { normalize } from '@/stores/drafts'

/**
 * readableConfig 生成工作台右栏那份**可读表示**。
 *
 * 它按设计稿的形式渲染，可读性优先——**它不是将要下发的字节**（ADR-0007）。
 * 真实 diff 只在「校验并推送」的确认弹层出现，两侧都来自后端渲染器。
 *
 * 因此这里不必、也不应该追求与后端逐字节一致：追求一致会让它变成第二个
 * 渲染器，而两个渲染器必然漂开，届时「以哪个为准」就成了问题。
 */
export function readableConfig(r: Route): string {
  const wl = normalize(r.wl)
  const lines: string[] = [
    `${r.domain} {`,
    `  回源        ${r.upstream}`,
    `  请求体上限  ${r.body_max}`,
    `  响应压缩    ${r.compress ? 'zstd + gzip' : '关闭'}`,
    `  回源 mTLS   ${r.mtls ? '开启（出示 edge-mtls 客户端证书）' : '关闭'}`,
  ]
  if (wl.length === 0 || (wl.length === 1 && wl[0] === '0.0.0.0/0')) {
    lines.push('  访问控制    放行所有来源')
  } else {
    lines.push(`  非白名单处置 ${r.block}`)
    lines.push('  白名单')
    for (const ip of wl) lines.push(`    - ${ip}`)
  }
  lines.push('}')
  return lines.join('\n')
}
