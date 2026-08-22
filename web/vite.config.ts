import { fileURLToPath, URL } from 'node:url'
import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'
import { wsMockPlugin } from './mocks/ws-plugin'

const MASTER = 'http://127.0.0.1:8080'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), 'VITE_')
  const useMock = env.VITE_USE_MOCK !== 'false'

  return {
    plugins: [
      vue(),
      // mock 与真主控互斥：接真 master 时不能让插件抢走 /api/v1/ws 的 upgrade
      ...(useMock ? [wsMockPlugin()] : []),
    ],
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url)),
        '~mocks': fileURLToPath(new URL('./mocks', import.meta.url)),
      },
    },
    server: {
      port: 5173,
      /*
       * 接真主控时必须走代理，不能让前端直连 :8080。
       * 会话 Cookie 是 HttpOnly + SameSite=Strict，跨端口就是跨站，浏览器不会带上它
       * —— 登录会「成功」然后每个请求都 401。代理之后前端与主控同源，Cookie 行为
       * 和生产一致（生产是 gin 直接托管前端产物，本来就同源）。
       */
      proxy: useMock
        ? undefined
        : {
            '/api/v1': {
              target: MASTER,
              changeOrigin: false,
              ws: true,
              /*
               * 主控不可达时，把代理错误降成一行可读的 warn，而不是整段
               * ECONNREFUSED 栈 —— backend 要反复重启 master 做后面几个切片，
               * 这条日志每天会出现很多次。
               *
               * 排查提示：如果 dev server 在你重启主控时跟着没了，先别怀疑这里。
               * `lsof -ti tcp:8080` 会连**带着到 8080 的出站连接**的进程一起列出来，
               * 而 Vite 的代理正好有一条 —— `lsof -ti tcp:8080 | xargs kill` 会把
               * Vite 自己也杀掉。只杀监听者：`lsof -ti tcp:8080 -sTCP:LISTEN`。
               */
              configure(proxy) {
                proxy.on('error', (err) => {
                  console.warn('[proxy] 主控暂时不可达：', err.message)
                })
                // upgrade 期间的错误发在 socket 上而不是 proxy 上，两个都要接
                proxy.on('proxyReqWs', (_proxyReq, _req, socket) => {
                  socket.on('error', () => {})
                })
              },
            },
          },
    },
    build: {
      outDir: 'dist',
      /*
       * 留着 sourcemap：控制台只在内网可达（登录页那句就是这么写的），
       * 而线上排障时一份 sourcemap 抵得上很多次猜。这是有意识的取舍，不是忘了关。
       */
      sourcemap: true,
      rollupOptions: {
        /*
         * **MSW 的 worker 不进生产包。**
         *
         * 它在 `public/` 下，vite 会原样拷进 dist。注册那段有 `import.meta.env.DEV`
         * 守着，所以它不会被启用 —— 但**它仍然会被部署到主控上并且可以被直接访问**。
         * 一个能拦截全站请求的 Service Worker 躺在生产目录里，不该有。
         *
         * 「不会被启用」和「不在那儿」是两件事，而只有后者不依赖那句 DEV 守卫
         * 一直正确。
         */
      },
    },
    // public 目录只有那一个文件，整个不拷比逐个排除干净
    publicDir: mode === 'production' ? false : 'public',
  }
})
