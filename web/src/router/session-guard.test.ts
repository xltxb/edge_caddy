import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, type Router, type RouteRecordRaw } from 'vue-router'
import { http } from '@/api/http'
import { installSessionGuards, routes } from './index'

/*
 * 测的是一件事：**会话过期之后，人能回到登录表单前面。**
 *
 * 这条路上有两个部件，各自都对：http 层收到 401 就喊一声「跳登录页」，
 * 路由守卫说「已登录就别停在登录页」。合起来会把人锁在外面——过期那一刻
 * 没有任何人把 session 置空，于是守卫读到的还是过期前那个用户名，
 * 一跳一弹，每一轮再触发一次 401（issue #38）。
 *
 * 所以断言落在**最终停在哪一页**上，而不是 session store 的字段上：
 * 字段怎么表达是实现的事，人看不看得到登录框不是。
 */

// 真 router 的视图全是懒加载的，把它们拖起来对一条守卫测试毫无意义。
// 路由表用真的那份（meta.public 就在里面），只把组件换成替身。
function stub(list: RouteRecordRaw[]): RouteRecordRaw[] {
  return list.map((r) => {
    const next = { ...r } as RouteRecordRaw & { component?: unknown }
    if ('component' in next) next.component = { template: '<div />' }
    return next
  })
}

function envelope(data: unknown, status = 200): Response {
  return new Response(JSON.stringify({ code: 0, msg: '', data }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('会话过期后的落点', () => {
  let router: Router

  beforeEach(() => {
    setActivePinia(createPinia())
    router = createRouter({ history: createMemoryHistory(), routes: stub(routes) })
    installSessionGuards(router)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('过期之后停在登录页，而不是被弹回内页', async () => {
    // 先正常登录进去：probe 拿到一个用户名。
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => envelope({ username: 'abiu' })),
    )
    await router.push('/overview')
    await router.isReady()
    expect(router.currentRoute.value.name).toBe('overview')

    // 会话在主控那边没了：此后任何请求都是 401。
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => envelope(null, 401)),
    )
    await expect(http.get('/nodes')).rejects.toThrow()

    // 跳转是 void 出去的，等路由这一轮走完。
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).toBe('login')
    })

    // 而且要停得住：再跑一轮守卫不会把人重新弹进去。
    await router.push('/login').catch(() => {})
    expect(router.currentRoute.value.name).toBe('login')
  })

  it('带着回跳目标，登录完能回到原来那一页', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => envelope({ username: 'abiu' })),
    )
    await router.push('/certs')
    await router.isReady()

    vi.stubGlobal(
      'fetch',
      vi.fn(async () => envelope(null, 401)),
    )
    await expect(http.get('/certs')).rejects.toThrow()

    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).toBe('login')
    })
    expect(router.currentRoute.value.query.redirect).toBe('/certs')
  })

  it('还没登录的人访问内页，照旧被送去登录页', async () => {
    // probe 拿不到人（401 走的是旁路方法，不触发全局跳转）。
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => envelope(null, 401)),
    )
    await router.push('/nodes').catch(() => {})
    expect(router.currentRoute.value.name).toBe('login')
  })
})
