import { expect, test } from '@playwright/test'
import { pendingBadge, resetMocks, withDraftSaved } from './helpers'

/**
 * 草稿语义的往返。
 *
 * 「改回与线上一致就删键」这条纯逻辑已经有单测（workbench/draft.test.ts），
 * 但那测不到它经过一次 `PUT /drafts/:key` 往返之后还成不成立 —— 而虚报一个
 * 待下发数字恰好长在「怕推错」这条主线上。
 */
test.describe('草稿', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/workbench')
    await resetMocks(page)
    await page.reload()
    await expect(page.getByText('反代路由').first()).toBeVisible()
  })

  test('改一个字段再改回去，待下发数回到原值', async ({ page }) => {
    await expect(pendingBadge(page)).toHaveText('3 处变更')

    // label 与控件是关联的，所以可以按标签找 —— 也顺带验了这层可达性
    const bodyMax = page.getByLabel('请求体上限')
    await bodyMax.fill('99MB')
    await expect(pendingBadge(page)).toHaveText('3 处变更') // 该字段本来就在草稿里，仍是 1 处

    // 改回 live 值：这个字段应当从草稿里被删掉（契约 §6.4）
    await withDraftSaved(page, () => bodyMax.fill('5MB'))
    await expect(pendingBadge(page)).toHaveText('2 处变更')

    // 刷新一次，确认写回后端的 Partial 也是干净的（不是只在内存里对）。
    // 这一步曾经抓到一个真 bug：节流窗口内刷新，那次写从没发出去过。
    await page.reload()
    await expect(pendingBadge(page)).toHaveText('2 处变更')
  })

  test('放弃改动清掉该资源的全部草稿', async ({ page }) => {
    await withDraftSaved(page, () => page.getByRole('button', { name: '放弃改动' }).click())
    await expect(pendingBadge(page)).toHaveText('1 处变更')
    await page.reload()
    await expect(pendingBadge(page)).toHaveText('1 处变更')
  })
})
