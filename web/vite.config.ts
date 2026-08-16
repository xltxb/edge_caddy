import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    proxy: {
      // 同源部署免 CORS（前端文档 §1）。开发时代理到本地主控。
      '/api': {
        target: process.env.VITE_MASTER ?? 'http://127.0.0.1:8080',
        changeOrigin: true,
        // ws:true 让 /api/v1/ws 的 Upgrade 握手也走代理，
        // 否则浏览器会直接连 5173 上并不存在的 WS 端点。
        ws: true,
      },
    },
  },
})
