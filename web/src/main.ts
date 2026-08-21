import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import './styles/tokens.css'
import './styles/reset.css'

async function bootstrap(): Promise<void> {
  // mock 层只在开发下介入，且可以用 VITE_USE_MOCK=false 关掉直连真主控。
  // 产品代码里没有任何假数据分支 —— 关掉开关，这一层就完全不存在。
  if (import.meta.env.DEV && import.meta.env.VITE_USE_MOCK !== 'false') {
    const { startMocks } = await import('~mocks/browser')
    await startMocks()
  }

  createApp(App).use(createPinia()).use(router).mount('#app')
}

void bootstrap()
