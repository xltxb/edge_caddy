import { test, expect } from '@playwright/test'
import { ADMIN_PASSWORD } from './global-setup'

// 冒烟：登录 → 节点页 → 拿到安装命令 → 11 个入口都能点开。
//
// 这条覆盖的是 #1 里两条无法用单测证明的验收项：登录跳转真的通，
// 以及导航没有死链。
test('登录后可用，且 11 个入口无死链', async ({ page }) => {
  // 未登录直奔内页应被送到登录页，并记住原目标
  await page.goto('/nodes')
  await expect(page).toHaveURL(/\/login\?redirect=\/nodes/)

  await page.getByLabel('口令').fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: '登录' }).click()

  // 登录后回到原本要去的页面，而不是首页
  await expect(page).toHaveURL(/\/nodes$/)
  await expect(page.getByText('还没有节点接入')).toBeVisible()

  // 「添加节点」给出的安装命令必须把 Token 放在环境变量里，
  // 而不是命令行参数（命令行参数会出现在 ps 输出里）
  await page.getByRole('button', { name: '添加节点' }).click()
  const cmd = page.locator('pre.cmd')
  await expect(cmd).toContainText('EDGE_ENROLL_TOKEN=')
  await expect(cmd).not.toContainText('--token')

  // 11 个一级入口全部可达，且不出现空白页
  for (const label of [
    '集群总览', '边缘节点', 'DNS 调度', '配置工作台', '反代路由',
    '访问控制', '证书', '下发记录', '审计日志', '告警通知', '系统设置',
  ]) {
    await page.getByRole('link', { name: label, exact: true }).click()
    await expect(page.locator('main')).not.toBeEmpty()
  }
})

test('深浅主题可切换', async ({ page }) => {
  await page.goto('/login')
  await page.getByLabel('口令').fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: '登录' }).click()
  await expect(page).toHaveURL(/\/overview$/)

  await page.getByRole('button', { name: '切换到深色' }).click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
})
