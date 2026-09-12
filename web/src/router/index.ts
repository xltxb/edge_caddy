import { createRouter, createWebHistory, type Router, type RouteRecordRaw } from 'vue-router'
import { setUnauthorizedHandler } from '@/api/http'
import { useSessionStore } from '@/stores/session'

export const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/overview' },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { layout: 'bare', public: true },
  },
  { path: '/overview', name: 'overview', component: () => import('@/views/OverviewView.vue') },
  { path: '/nodes', name: 'nodes', component: () => import('@/views/NodesView.vue') },
  { path: '/dns', name: 'dns', component: () => import('@/views/DnsView.vue') },
  { path: '/domains', name: 'domains', component: () => import('@/views/DomainsView.vue') },
  {
    path: '/workbench/:key?',
    name: 'workbench',
    component: () => import('@/views/WorkbenchView.vue'),
    /*
     * 前端开发文档 §3 说「离开且有草稿时守卫确认」。**不做这个守卫**：
     * 草稿是持久化在主控上的、全局可见的（契约 §6.4），离开页面不丢任何东西。
     * 为一件不会发生的损失弹确认框，只会训练人无脑点「确定」——真正需要确认的
     * 那一刻（下发）反而失去分量。
     */
  },
  { path: '/routes', name: 'routes', component: () => import('@/views/RoutesView.vue') },
  { path: '/acl', name: 'acl', component: () => import('@/views/AclView.vue') },
  { path: '/certs', name: 'certs', component: () => import('@/views/CertsView.vue') },
  { path: '/deploys', name: 'deploys', component: () => import('@/views/DeploysView.vue') },
  { path: '/audit', name: 'audit', component: () => import('@/views/AuditView.vue') },
  { path: '/alerts', name: 'alerts', component: () => import('@/views/AlertsView.vue') },
  { path: '/settings', name: 'settings', component: () => import('@/views/SettingsView.vue') },
  { path: '/:pathMatch(.*)*', redirect: '/overview' },
]

/**
 * installSessionGuards 把「谁能进哪一页」这件事装到一个 router 上。
 *
 * 抽成函数而不是写成模块级副作用，是为了让它能被真的测到：模块级的那份只跟着
 * 真 router 走，而真 router 拿的是 createWebHistory 和一堆懒加载的视图，
 * 一条守卫测试要为此把整个控制台拖起来。这里换成内存 history + 替身组件，
 * **守卫本身还是同一份**。
 */
export function installSessionGuards(r: Router): void {
  // http 层不认识 router，会话失效时由这里接手（契约 §0.2：401 是唯一需要特判的码）
  setUnauthorizedHandler(() => {
    const session = useSessionStore()
    /*
     * **先把会话置空，再跳转。**
     *
     * 原先这里只跳不清，而 beforeEach 只在 `!session.resolved` 时才去 probe——
     * 启动那一次早就把 resolved 置成 true 了。于是到了 /login（public 路由），
     * 守卫读到的 operator 还是过期前那个用户名，一句「已登录就别停在登录页」
     * 把人弹回 /overview，那一页的请求再 401，再跳回来……登录表单一次都出不来，
     * 人只能手动清站点数据（issue #38）。
     *
     * 置空 operator 就够了：守卫判的是 operator，不是 resolved。
     * 而 resolved 保持 true，避免跳到登录页时再问一次后端「我登录了吗」——
     * 那个问题刚刚已经被 401 回答过了。
     */
    session.expire()

    const current = r.currentRoute.value
    if (current.name === 'login') return
    void r.replace({ name: 'login', query: { redirect: current.fullPath } })
  })

  r.beforeEach(async (to) => {
    const session = useSessionStore()
    if (!session.resolved) await session.probe()

    if (to.meta.public) {
      // 已登录就别停在登录页
      return session.operator ? { path: '/overview' } : true
    }
    if (!session.operator) {
      return { name: 'login', query: { redirect: to.fullPath } }
    }
    return true
  })
}

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

installSessionGuards(router)
