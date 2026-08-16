import { test, expect } from '@playwright/test'
import { ADMIN_PASSWORD } from './global-setup'

async function login(page: import('@playwright/test').Page) {
  await page.goto('/login')
  await page.getByLabel('口令').fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: '登录' }).click()
  await expect(page).toHaveURL(/\/overview$/)
}

// 改回源 → 右栏出现 diff → 改回原值 diff 消失 → 确认弹层给出后端权威 diff。
//
// 「改回原值 diff 消失」这一条是 ADR 之外最容易退化的地方：留着幽灵改动时
// 界面看起来完全正常，只有推不掉才会被发现。
test('工作台：草稿、可读 diff、改回原值即归零', async ({ page }) => {
  await login(page)

  // 先建一条路由
  await page.getByRole('link', { name: '反代路由', exact: true }).click()
  await page.getByRole('button', { name: '新建路由' }).click()
  await page.getByLabel('域名').fill(`wb-${Date.now()}.example.com`)
  await page.getByLabel('回源地址').fill('10.8.0.2:8080')
  await page.getByRole('button', { name: '保存' }).click()
  await expect(page.getByText(/wb-\d+\.example\.com/)).toBeVisible()

  await page.getByRole('link', { name: '配置工作台', exact: true }).click()
  await expect(page.getByText('没有待推送的变更')).toBeVisible()

  // 改回源：右栏出现变更、底栏计数
  const upstream = page.getByLabel('回源地址')
  await upstream.fill('10.0.0.9:9090')
  await expect(page.getByText(/1 个资源共 1 处未推送/)).toBeVisible()
  await expect(page.locator('.dl.add')).toContainText('10.0.0.9:9090')

  // 改回原值：草稿键被删掉，一切归零
  await upstream.fill('10.8.0.2:8080')
  await expect(page.getByText('没有待推送的变更')).toBeVisible()
  await expect(page.locator('.dl.add')).toHaveCount(0)
})

// 确认弹层展示的是后端权威渲染，与右栏的可读表示**不是**同一份东西。
test('确认弹层给出后端权威 diff', async ({ page }) => {
  await login(page)
  await page.getByRole('link', { name: '反代路由', exact: true }).click()
  await page.getByRole('button', { name: '新建路由' }).click()
  await page.getByLabel('域名').fill(`auth-${Date.now()}.example.com`)
  await page.getByLabel('回源地址').fill('10.8.0.2:8080')
  await page.getByRole('button', { name: '保存' }).click()

  await page.getByRole('link', { name: '配置工作台', exact: true }).click()
  await page.getByLabel('回源地址').fill('10.0.0.9:9090')
  await page.getByRole('button', { name: '校验并推送' }).click()

  const modal = page.locator('.modal')
  await expect(modal).toBeVisible()
  await expect(modal).toContainText('后端权威渲染')

  // 改回源在行级 diff 里只会改动 "dial" 那一行——reverse_proxy 在未变更行上。
  // 断言变更行的真实内容，而不是它周围的结构。
  await expect(modal.locator('.dl.add')).toContainText('10.0.0.9:9090')
  await expect(modal.locator('.dl.del')).toContainText('10.8.0.2:8080')
  // 权威 diff 是真实 Caddy 配置：出现 JSON 键名，而不是可读表示的中文标签
  await expect(modal.locator('.dbody')).toContainText('dial')
  await expect(modal.locator('.dbody')).not.toContainText('请求体上限')

  // 没有在线节点，推送应如实报错而不是假成功
  await modal.getByRole('button', { name: /推送选中的/ }).click()
  await expect(modal.getByText(/没有在线节点/)).toBeVisible()
})
