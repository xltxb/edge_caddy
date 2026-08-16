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

// 建路由 → 下发。这条跑的是**真后端**，因此它同时守住「前后端两处校验漂开」：
// 表单放行但服务端拒绝时，保存那一步会红。
test('可以建路由；没有节点时下发如实报错', async ({ page }) => {
  await page.goto('/login')
  await page.getByLabel('口令').fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: '登录' }).click()

  await page.getByRole('link', { name: '反代路由', exact: true }).click()
  await expect(page.getByText('还没有路由')).toBeVisible()

  await page.getByRole('button', { name: '新建路由' }).click()

  // 非法回源应即时标红并禁用保存
  await page.getByLabel('域名').fill('api.example.com')
  await page.getByLabel('回源地址').fill('10.8.0.2')
  await expect(page.getByText('必须带端口')).toBeVisible()
  await expect(page.getByRole('button', { name: '保存' })).toBeDisabled()

  // 补上端口后可保存，且服务端确实接受——前后端校验一致才走得到这一步
  await page.getByLabel('回源地址').fill('10.8.0.2:8080')
  await page.getByRole('button', { name: '保存' }).click()
  await expect(page.getByText('api.example.com')).toBeVisible()
  await expect(page.getByText('未下发')).toBeVisible()

  // 没有任何节点在线时，下发必须如实报错，而不是报告「成功推给 0 个节点」
  await page.getByRole('button', { name: '校验并推送' }).click()
  await expect(page.getByText(/没有在线节点/)).toBeVisible()
})
