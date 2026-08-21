import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { setUnauthorizedHandler } from '@/api/http'
import { useSessionStore } from '@/stores/session'

const routes: RouteRecordRaw[] = [
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

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

// http 层不认识 router，会话失效时由这里接手（契约 §0.2：401 是唯一需要特判的码）
setUnauthorizedHandler(() => {
  const current = router.currentRoute.value
  if (current.name === 'login') return
  void router.replace({ name: 'login', query: { redirect: current.fullPath } })
})

router.beforeEach(async (to) => {
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
