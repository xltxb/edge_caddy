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

/** 顶栏「待下发」当前显示的文本。 */
export function pendingBadge(page: Page) {
  return page.locator('header').getByText('待下发').locator('xpath=following-sibling::b[1]')
}
