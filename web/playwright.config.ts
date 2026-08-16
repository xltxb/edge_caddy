import { defineConfig } from '@playwright/test'

/**
 * 冒烟用真浏览器打真前端。后端由 tests/e2e/global-setup 起一个真的 master——
 * 不 mock 接口：这条测试的价值恰恰在于验证前后端拼在一起能跑，
 * mock 掉接口就只剩「我 mock 的数据能渲染」。
 */
export default defineConfig({
  testDir: './tests/e2e',
  timeout: 30_000,
  globalSetup: './tests/e2e/global-setup.ts',
  use: { baseURL: 'http://127.0.0.1:5199', trace: 'off' },
  webServer: {
    command: 'npx vite --host 127.0.0.1 --port 5199',
    url: 'http://127.0.0.1:5199',
    reuseExistingServer: false,
    timeout: 60_000,
    env: { VITE_MASTER: 'http://127.0.0.1:8099' },
  },
})
