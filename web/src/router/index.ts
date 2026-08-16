import { createRouter, createWebHistory, type Router, type RouteRecordRaw } from 'vue-router'

/**
 * 路由表（前端文档 §3）。11 个一级入口与左导航一一对应。
 *
 * 首切片只有 login / overview / nodes 有实质内容，其余是明确标注「尚未实现」的
 * 占位页——而不是死链。死链会让人以为是坏了，占位页告诉人这里还没做。
 */
export const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/overview' },
  { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true } },
  { path: '/overview', name: 'overview', component: () => import('@/views/OverviewView.vue') },
  { path: '/nodes', name: 'nodes', component: () => import('@/views/NodesView.vue') },
  { path: '/dns', name: 'dns', component: () => import('@/views/PlaceholderView.vue'), meta: { title: 'DNS 调度', issue: 15 } },
  { path: '/workbench/:key?', name: 'workbench', component: () => import('@/views/WorkbenchView.vue'), meta: { title: '配置工作台' } },
  { path: '/routes', name: 'routes', component: () => import('@/views/RoutesView.vue'), meta: { title: '反代路由' } },
  { path: '/acl', name: 'acl', component: () => import('@/views/PlaceholderView.vue'), meta: { title: '访问控制', issue: 7 } },
  { path: '/certs', name: 'certs', component: () => import('@/views/PlaceholderView.vue'), meta: { title: '证书', issue: 14 } },
  { path: '/deploys', name: 'deploys', component: () => import('@/views/DeploysView.vue'), meta: { title: '下发记录' } },
  { path: '/audit', name: 'audit', component: () => import('@/views/AuditView.vue'), meta: { title: '审计日志' } },
  { path: '/alerts', name: 'alerts', component: () => import('@/views/PlaceholderView.vue'), meta: { title: '告警通知', issue: 12 } },
  { path: '/settings', name: 'settings', component: () => import('@/views/PlaceholderView.vue'), meta: { title: '系统设置', issue: 0 } },
]

/** installGuard 装上登录守卫。抽出来是为了让测试能装到自己的路由器上。 */
export function installGuard(router: Router) {
  router.beforeEach(async (to) => {
    const { useSessionStore } = await import('@/stores/session')
    const session = useSessionStore()
    if (!session.ready) await session.refresh()

    if (to.meta.public) {
      // 已登录还去登录页就直接回首页
      return session.needsLogin() ? true : { path: '/overview' }
    }
    if (!session.needsLogin()) return true
    // 记住原目标：登录完要回到这里，而不是把人丢到首页再点一次
    return { path: '/login', query: { redirect: to.fullPath } }
  })
}

export function createAppRouter() {
  const router = createRouter({ history: createWebHistory(), routes })
  installGuard(router)
  return router
}
