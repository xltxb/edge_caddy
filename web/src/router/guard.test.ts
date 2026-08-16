import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import { routes, installGuard } from './index'
import { useSessionStore } from '@/stores/session'

// 每个用例自己造一个路由器，避免用例之间共享导航历史。
function newRouter() {
  const r = createRouter({ history: createMemoryHistory(), routes })
  installGuard(r)
  return r
}

// meFn 替换掉真实的 /me 请求。这里替的是**网络边界**，不是内部协作者——
// 守卫逻辑本身完全跑真的。
function stubMe(result: { user: string; auth_required: boolean } | 'unauthorized') {
  const session = useSessionStore()
  session.__setFetcher(async () => {
    if (result === 'unauthorized') throw new Error('未登录或登录已过期')
    return result
  })
}

describe('路由守卫', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('未登录访问受保护页面时跳转登录页', async () => {
    stubMe('unauthorized')
    const router = newRouter()
    await router.push('/nodes')
    expect(router.currentRoute.value.path).toBe('/login')
  })

  // 跳转登录页时必须记住原目标，登录后回到那里。
  //
  // 不记住的话，用户点了「边缘节点」的链接、登录完却被丢到首页，
  // 得再点一次——这类小事累积起来就是「这个后台真难用」。
  it('登录后回到原本要去的页面', async () => {
    stubMe('unauthorized')
    const router = newRouter()
    await router.push('/nodes')
    expect(router.currentRoute.value.query.redirect).toBe('/nodes')

    // 登录成功后再导航
    stubMe({ user: 'abiu', auth_required: true })
    const session = useSessionStore()
    await session.refresh()
    await router.push(String(router.currentRoute.value.query.redirect))
    expect(router.currentRoute.value.path).toBe('/nodes')
  })

  it('已登录时访问登录页直接回首页', async () => {
    stubMe({ user: 'abiu', auth_required: true })
    const router = newRouter()
    await router.push('/login')
    expect(router.currentRoute.value.path).toBe('/overview')
  })

  // 主控未设置口令时鉴权是关的，此时不该把人挡在登录页外面。
  //
  // 后端的 /me 会如实回报 auth_required=false；守卫必须信它，
  // 否则会出现「没有口令可填，却被要求登录」的死锁。
  it('鉴权未启用时直接放行', async () => {
    stubMe({ user: 'anonymous', auth_required: false })
    const router = newRouter()
    await router.push('/nodes')
    expect(router.currentRoute.value.path).toBe('/nodes')
  })
})
