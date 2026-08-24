import type { Page } from '@playwright/test'

/**
 * 把 mock 状态复位到 seed。
 *
 * 下发会消费草稿、推进 version，用例之间会互相影响。让每个用例自己复位，
 * 比让它们小心地共享一份漂移的状态可靠得多 —— 后者的失败会表现为
 * 「单独跑绿、一起跑红」，那种问题查起来最费时间。
 */
export async function resetMocks(page: Page): Promise<void> {
  const res = await page.request.post('/api/v1/__test/reset')
  if (!res.ok()) throw new Error(`mock 复位失败：HTTP ${res.status()}`)
}

/**
 * 执行一个会改草稿的动作，并等它**真的写回**后端再返回。
 *
 * 草稿写回有 400ms 节流。不等就 reload 的话，卸载时的 keepalive PUT
 * 与新页面的 GET /drafts 谁先到服务端没有保证 —— 测试会时绿时红，
 * 而那种失败最费时间。等 PUT 的响应，断言的就是「写下去之后的状态」。
 */
export async function withDraftSaved(page: Page, action: () => Promise<void>): Promise<void> {
  const saved = page.waitForResponse(
    (r) => r.url().includes('/api/v1/drafts/') && r.request().method() === 'PUT',
    { timeout: 5_000 },
  )
  await action()
  await saved
}

/** 顶栏「待下发」当前显示的文本。 */
export function pendingBadge(page: Page) {
  return page.locator('header').getByText('待下发').locator('xpath=following-sibling::b[1]')
}

/**
 * 走到某一页，**走 SPA 内部路由，不用 `page.goto` / `page.reload`**。
 *
 * 两个原因，都是撞出来的：
 *
 * 1. **整页导航会把 MSW 的 seed 清回初始值。** handlers 活在浏览器里，页面一
 *    重载就重新初始化 —— 前一步用 `PUT` 存进去的东西全没了。症状不是「设置
 *    失败」，是**这一页读到的还是上一轮的值**，而那看起来像界面渲染错了。
 *    （Node 侧 vite 插件提供的那些端点不受影响，它们的状态在 Node 进程里。）
 *
 * 2. **同一路径 `push` 不会重新挂载组件**，于是它不会重新 GET。
 *    所以已经在目标页时先绕一下 —— 这正是「改了服务端状态、要这一页重新读」
 *    的那个场景。
 *
 * 顺带这也更接近人实际做的事：点左边的菜单，而不是敲地址栏。
 */
export async function visit(page: Page, path: string, ready: string): Promise<void> {
  if (new URL(page.url()).pathname === path) {
    await page.click('nav a[href="/overview"]')
    await page.waitForURL('**/overview')
  }
  await page.click(`nav a[href="${path}"]`)
  await page.waitForURL(`**${path}`)
  await page.waitForSelector(ready)
}
