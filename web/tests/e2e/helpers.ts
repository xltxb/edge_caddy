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
