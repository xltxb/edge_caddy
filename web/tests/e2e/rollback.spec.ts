import { expect, test } from '@playwright/test'
import { resetMocks } from './helpers'

/**
 * 回滚 —— 前端开发文档 §7 要求必须有 e2e 的另一条关键流程。
 *
 * 最要紧的性质是**回滚不直接下发**（契约 §7.5）：它把差异写回草稿，由人在
 * 工作台确认后走同一条流水线。如果哪天有人把它改成直接下发，这条用例要红。
 */
test.describe('回滚', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/deploys')
    await resetMocks(page)
    await page.reload()
    await expect(page.getByText('下发记录').first()).toBeVisible()
  })

  test('当前基线不可回滚', async ({ page }) => {
    // 回到自己是空操作。给一个能点但什么也不做的按钮只会让人怀疑系统坏了。
    const baselineRow = page.getByRole('row').filter({ hasText: '当前基线' })
    await expect(baselineRow.getByRole('button', { name: '回滚' })).toBeDisabled()
  })

  test('回滚写回草稿，不直接下发，并列出覆盖不到的资源', async ({ page }) => {
    const row = page.getByRole('row').filter({ hasText: 'cfg-8b03e7' })
    await row.getByRole('button', { name: '回滚' }).click()

    // 覆盖不到的资源必须显示出来。静默跳过 = 界面说成功了而某条路由其实没回去。
    const panel = page.getByText(/个资源已写回草稿/)
    await expect(panel).toBeVisible()
    await expect(page.getByText('回滚不会删除它')).toBeVisible()

    // 有 skipped 时**不自动跳转** —— 跳走会把这条警告一起扫掉
    await expect(page).toHaveURL(/\/deploys$/)

    await page.getByRole('button', { name: '去工作台确认已写回的' }).click()
    await expect(page).toHaveURL(/\/workbench/)

    // 写回的是草稿，不是已下发的配置
    await expect(page.getByText(/处未下发改动/).first()).toBeVisible()
  })

  test('逐节点结果里区分重试中与终态', async ({ page }) => {
    const row = page.getByRole('row').filter({ hasText: 'cfg-2f9a1c' })
    await row.getByRole('button', { name: '逐节点结果' }).click()

    await expect(page.getByText('deadline exceeded')).toBeVisible()
    await expect(page.getByText('重试中').first()).toBeVisible()
    await expect(page.getByText('不代表流量已经在走')).toBeVisible()
  })
})
