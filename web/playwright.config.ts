import { defineConfig, devices } from '@playwright/test'

/**
 * e2e 跑在 **mock 模式**上（MSW + Vite 插件），不依赖真主控。
 *
 * 端口用 5174 而不是 5173：开发时那个 dev server 通常开着，而且可能正跑在
 * `dev:real` 模式下连着真后端。抢同一个端口会让 e2e 的结果取决于「你此刻
 * 恰好开着哪个模式」，那种不确定性比多开一个端口贵得多。
 */
export default defineConfig({
  testDir: './tests/e2e',
  // 用例之间共享 mock 状态（下发会消费草稿），所以串行跑，每个用例自己复位
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? 'list' : [['list'], ['html', { open: 'never' }]],
  timeout: 30_000,
  expect: { timeout: 10_000 },
  use: {
    baseURL: 'http://localhost:5174',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: 'pnpm dev --port 5174 --strictPort',
    url: 'http://localhost:5174',
    reuseExistingServer: false,
    timeout: 60_000,
    stdout: 'ignore',
    stderr: 'pipe',
  },
})
