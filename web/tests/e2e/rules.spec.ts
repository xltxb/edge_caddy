import { expect, test } from '@playwright/test'
import { pendingBadge, resetMocks, withDraftSaved } from './helpers'

test.describe('删除访问规则', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/acl')
    await resetMocks(page)
    await page.reload()
  })

  test('确认框说清「删除不会立刻停止拦截」', async ({ page }) => {
    const row = page.locator('tr', { hasText: '合作方服务密钥' })
    await row.getByRole('button', { name: '删除' }).click()

    const dialog = page.getByRole('dialog', { name: '删除访问规则' })
    // 这条规则绑着域名 —— 那个时间差必须说出来，否则人删完会去试一个
    // 「应该能过」的请求，然后发现它还被拦着
    await expect(dialog).toContainText('删除不会立刻停止拦截')
    await expect(dialog).toContainText('api.example.com')

    await dialog.getByRole('button', { name: '取消' }).click()
    await expect(page.getByText('合作方服务密钥')).toBeVisible()
  })

  /*
   * 反面那一支。
   *
   * 第一版这条测试的**名字和内容说的是两回事** —— 名字说「未绑定的不说那句话」，
   * 内容却拿一条绑着域名的规则断言它说了。根因是 seed 里没有未绑定的规则：
   * 一个夹具表达不了的状态，写不出针对它的测试，于是名字先跑到了内容前面。
   */
  test('未绑定域名的规则不说那句话 —— 它本来就不生效', async ({ page }) => {
    const row = page.locator('tr', { hasText: '预发环境白名单' })
    await row.getByRole('button', { name: '删除' }).click()

    const dialog = page.getByRole('dialog', { name: '删除访问规则' })
    await expect(dialog).toContainText('本来就不生效')
    await expect(dialog).not.toContainText('删除不会立刻停止拦截')
    await dialog.getByRole('button', { name: '取消' }).click()
  })

  /*
   * 这条是这组的理由。
   *
   * 删除会连同该资源的草稿一起清掉。不清的话，顶栏的「待下发」会一直算着一个
   * **再也下发不出去**的东西——而那个数字正好长在「怕推错」这条主线上。
   */
  test('删掉带草稿的规则，待下发数跟着降', async ({ page }) => {
    await page.goto('/workbench/rule:office-wl')
    // seed 预置了 2 条路由草稿共 3 处改动（与 draft.spec.ts 同源）
    await expect(pendingBadge(page)).toHaveText('3 处变更')

    // 给这条规则也制造一处未下发改动
    await withDraftSaved(page, async () => {
      await page.getByLabel('允许的来源 IP').fill('203.0.113.7\n198.51.100.24')
    })
    await expect(pendingBadge(page)).toHaveText('4 处变更')

    await page.goto('/acl')
    await page.locator('tr', { hasText: '办公出口白名单' }).getByRole('button', { name: '删除' }).click()
    await page
      .getByRole('dialog', { name: '删除访问规则' })
      .getByRole('button', { name: '确认删除' })
      .click()

    await expect(page.locator('tr', { hasText: '办公出口白名单' })).toHaveCount(0)
    // 回到 3：那条规则的草稿跟着它一起没了，而不是留成一个下发不出去的数字
    await expect(pendingBadge(page)).toHaveText('3 处变更')
  })
})
