import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { createRouter, createMemoryHistory, type Router } from 'vue-router'
import WorkbenchView from './WorkbenchView.vue'
import CommandPalette from '@/components/console/CommandPalette.vue'
import { useDraftsStore } from '@/stores/drafts'
import { useRoutesStore } from '@/stores/routes'
import { useNodesStore } from '@/stores/nodes'
import { routes as appRoutes } from '@/router'
import type { Route } from '@/api/types'

const live: Route[] = [
  { domain: 'api.example.com', upstream: '10.0.0.1:8080', block: 'abort', mtls: false,
    compress: true, body_max: '5MB', wl: ['0.0.0.0/0'], ver: 3 },
  { domain: 'shop.example.com', upstream: '10.0.0.2:8080', block: '403', mtls: false,
    compress: false, body_max: '1MB', wl: ['0.0.0.0/0'], ver: 1 },
]

let pinia: Pinia
let router: Router

async function seed() {
  const d = useDraftsStore()
  // 直接喂进 store 的状态，绕开网络：这条测试要验的是「跳过去有没有选中」，
  // 不是加载
  d.live = live
  d.drafts = {}
  vi.spyOn(d, 'load').mockResolvedValue(undefined)
  const r = useRoutesStore()
  r.routes = live
  vi.spyOn(r, 'load').mockResolvedValue(undefined)
  // 面板唤起时会补拉节点列表，这里也堵上，免得测试去打真网络
  const n = useNodesStore()
  vi.spyOn(n, 'load').mockResolvedValue(undefined)
  return d
}

const mountOpts = () => ({ global: { plugins: [pinia, router] } })

describe('工作台按 URL 选中条目', () => {
  beforeEach(async () => {
    pinia = createPinia()
    setActivePinia(pinia)
    router = createRouter({ history: createMemoryHistory(), routes: appRoutes })
  })

  // 带 :key 进来就选中那一条。
  //
  // 不读这个参数的话，命令面板敲域名跳过来会落在列表第一条上——
  // 人以为自己打开了 shop，改的却是 api。
  it('URL 里带资源键时选中对应的路由', async () => {
    await seed()
    router.push('/workbench/route%3Ashop.example.com')
    await router.isReady()

    const w = mount(WorkbenchView, mountOpts())
    await w.vm.$nextTick()
    await w.vm.$nextTick()

    expect(w.find('[data-testid="current-domain"]').text()).toBe('shop.example.com')
  })

  it('没带资源键时退回第一条', async () => {
    await seed()
    router.push('/workbench')
    await router.isReady()

    const w = mount(WorkbenchView, mountOpts())
    await w.vm.$nextTick()
    await w.vm.$nextTick()

    expect(w.find('[data-testid="current-domain"]').text()).toBe('api.example.com')
  })

  // 资源键指向一条不存在的路由时不能装作选中了。
  it('资源键对不上任何路由时明确提示，不静默落到第一条', async () => {
    await seed()
    router.push('/workbench/route%3Aghost.example.com')
    await router.isReady()

    const w = mount(WorkbenchView, mountOpts())
    await w.vm.$nextTick()
    await w.vm.$nextTick()

    expect(w.text()).toContain('ghost.example.com')
    expect(w.find('[data-testid="current-domain"]').text()).not.toBe('api.example.com')
  })

  // 端到端那一小段：面板敲域名 → Enter → 工作台选中那条。
  it('命令面板敲域名后跳到工作台并选中那条路由', async () => {
    await seed()
    router.push('/overview')
    await router.isReady()

    const palette = mount(CommandPalette, mountOpts())
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true }))
    await palette.vm.$nextTick()
    await palette.find('input').setValue('shop.example')
    await palette.find('input').trigger('keydown', { key: 'Enter' })

    const t0 = Date.now()
    while (router.currentRoute.value.path === '/overview' && Date.now() - t0 < 2000) {
      await new Promise((r) => setTimeout(r))
    }
    expect(router.currentRoute.value.path).toBe('/workbench/route%3Ashop.example.com')

    const w = mount(WorkbenchView, mountOpts())
    await w.vm.$nextTick()
    await w.vm.$nextTick()
    expect(w.find('[data-testid="current-domain"]').text()).toBe('shop.example.com')
  })

// 已经打开的工作台上再跳一次，选中项要跟着换。
//
// 这条是变异测试暴露的缺口：其余用例每次都重新挂载组件，于是「URL 变了不重选」
// 这个错误实现照样全绿。而人已经在工作台里再敲一次 ⌘K 时，组件根本不会重新挂载。
it('在已打开的工作台上换 URL，选中项跟着换', async () => {
  await seed()
  router.push('/workbench/route%3Aapi.example.com')
  await router.isReady()

  const w = mount(WorkbenchView, mountOpts())
  await w.vm.$nextTick()
  await w.vm.$nextTick()
  expect(w.find('[data-testid="current-domain"]').text()).toBe('api.example.com')

  await router.push('/workbench/route%3Ashop.example.com')
  await w.vm.$nextTick()
  await w.vm.$nextTick()
  expect(w.find('[data-testid="current-domain"]').text()).toBe('shop.example.com')
})
})
