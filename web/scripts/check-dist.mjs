#!/usr/bin/env node
/**
 * 生产产物里不该有的东西。
 *
 * 起因：第一次打包时 `mockServiceWorker.js` 混进了包里。注册那段有
 * `import.meta.env.DEV` 守着，所以它不会被启用 —— 但**它仍然会被部署到主控上
 * 并且可以被直接访问**。一个能拦截全站请求的 Service Worker 躺在生产目录里，
 * 不该有。
 *
 * **「不会被启用」和「不在那儿」是两件事**，而只有后者不依赖那句 DEV 守卫
 * 一直正确。
 *
 *   node scripts/check-dist.mjs      # 会先构建
 */
import { readFileSync, existsSync } from 'node:fs'
import { execSync } from 'node:child_process'

execSync('npx vite build', { stdio: 'pipe' })

const files = execSync('find dist -type f', { encoding: 'utf8' }).split('\n').filter(Boolean)

/*
 * 正面自检先跑：这是否定断言（「不该有 X」），构建失败或 dist 是空的时候
 * 它会**因为什么都没扫到而变绿**。
 */
if (files.length < 10) {
  console.error(`\n✗ dist 里只有 ${files.length} 个文件 —— 构建多半没成，下面的结论不作数\n`)
  process.exit(2)
}
if (!existsSync('dist/index.html')) {
  console.error('\n✗ 没有 index.html —— 构建产物不完整\n')
  process.exit(2)
}

const BANNED = [
  { re: /mockServiceWorker/, why: 'MSW 的 worker —— 能拦截全站请求，不该躺在生产目录里' },
  { re: /\.env(\.|$)/, why: '环境文件' },
  { re: /\/mocks?\//, why: 'mock 层的源码' },
]

const hits = []
for (const f of files) {
  for (const b of BANNED) if (b.re.test(f)) hits.push(`${f}\n      ${b.why}`)
}

/*
 * 深层路由要靠伺服方做 SPA fallback —— index.html 里的资源路径是**根绝对路径**，
 * 这一条顺带钉住那个事实：路径要是哪天变成相对的，部署说明里那段 fallback 就
 * 该跟着改。（真起过静态服务器验过：没有 fallback 时 /nodes 是 404。）
 */
const html = readFileSync('dist/index.html', 'utf8')
if (!/src="\/assets\//.test(html)) {
  hits.push('index.html 的资源路径不再是根绝对路径 —— 部署说明里的 SPA fallback 那段要跟着改')
}

if (hits.length) {
  console.error(`\n${hits.length} 处不该出现在生产产物里：\n\n  ${hits.join('\n  ')}\n`)
  process.exit(1)
}
console.log(`\n${files.length} 个产物文件，没有不该有的东西。\n`)
