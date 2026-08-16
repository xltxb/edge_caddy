import { test, expect } from '@playwright/test'
import { ADMIN_PASSWORD } from './global-setup'

async function login(page: import('@playwright/test').Page) {
  await page.goto('/login')
  await page.getByLabel('口令').fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: '登录' }).click()
  await expect(page).toHaveURL(/\/overview$/)
}

// ⌘K 敲域名 → Enter → 落到工作台的那一条上。
//
// 真浏览器跑这一条，是因为它串起来的三件事各自都在别处测过、
// 而串起来的地方（快捷键绑定、路由参数编码、工作台读参数）没有一处是纯逻辑的。
test('命令面板：敲域名跳到工作台并选中那条路由', async ({ page }) => {
  await login(page)

  const domain = `pal-${Date.now()}.example.com`
  await page.getByRole('link', { name: '反代路由', exact: true }).click()
  await page.getByRole('button', { name: '新建路由' }).click()
  await page.getByLabel('域名').fill(domain)
  await page.getByLabel('回源地址').fill('10.8.0.7:8080')
  await page.getByRole('button', { name: '保存' }).click()
  await expect(page.getByText(domain)).toBeVisible()

  // 从总览页唤起，验证它是全局的
  await page.getByRole('link', { name: '集群总览', exact: true }).click()
  await page.keyboard.press('ControlOrMeta+k')
  const input = page.getByLabel('命令', { exact: true })
  await expect(input).toBeVisible()

  await input.fill(domain)
  await expect(page.getByText(`前往路由 ${domain}`)).toBeVisible()
  await input.press('Enter')

  await expect(page).toHaveURL(new RegExp(`/workbench/route%3A${domain.replace(/\./g, '\\.')}$`))
  await expect(page.getByTestId('current-domain')).toHaveText(domain)
})

// Esc 关闭，且没执行任何东西。
test('命令面板：Esc 关闭不执行', async ({ page }) => {
  await login(page)
  await page.keyboard.press('ControlOrMeta+k')
  const input = page.getByLabel('命令', { exact: true })
  await expect(input).toBeVisible()
  await input.fill('drain')
  await input.press('Escape')
  await expect(input).toBeHidden()
  // 确认框不该冒出来
  await expect(page.getByTestId('drain-confirm')).toHaveCount(0)
})
