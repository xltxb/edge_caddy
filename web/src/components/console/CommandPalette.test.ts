import { describe, it, expect, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { createRouter, createMemoryHistory, type Router } from 'vue-router'
import CommandPalette from './CommandPalette.vue'
import DrainConfirm from './DrainConfirm.vue'
import NodesView from '@/views/NodesView.vue'
import { useNodesStore } from '@/stores/nodes'
import { routes } from '@/router'
import type { NodesResponse } from '@/api/types'

const seed: NodesResponse = {
  baseline: 'cfg-2f9a1c',
  nodes: [
    { id: 'node-hk-01', city: '香港', vendor: 'DMIT PPro', line: 'CN2 GIA', ip: '103.117.44.18',
      status: 'ok', cfg: 'cfg-2f9a1c', dns: true, drifted: false, last_hb: '2026-08-16T04:00:00Z' },
    { id: 'node-us-01', city: '洛杉矶', vendor: 'Contabo', line: '国际 BGP', ip: '194.238.19.62',
      status: 'down', cfg: 'cfg-8b03e7', dns: false, drifted: true, last_hb: '2026-08-16T03:50:00Z' },
  ],
  drifted: ['node-us-01'],
}

let pinia: Pinia
let router: Router
let posted: string[]

async function ready() {
  posted = []
  const s = useNodesStore()
  s.__setFetcher(async () => seed)
  s.__setPoster(async (path) => {
    posted.push(path)
    return { detail: '3ms' }
  })
  await s.load()
  return s
}

const mountOpts = () => ({ global: { plugins: [pinia, router] } })

/** until 轮询等待条件成立。路由跳转要等目标组件的动态 import，
 *  硬睡固定毫秒数是 flaky 的来源。 */
async function until(cond: () => boolean, ms = 2000) {
  const t0 = Date.now()
  while (!cond() && Date.now() - t0 < ms) {
    await new Promise((r) => setTimeout(r))
  }
  return cond()
}

/** openPalette 挂载面板并用真的 ⌘K 唤起它——面板默认是关着的。 */
async function openPalette() {
  const w = mount(CommandPalette, mountOpts())
  window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true }))
  await w.vm.$nextTick()
  return w
}

describe('命令面板与行内按钮', () => {
  beforeEach(async () => {
    pinia = createPinia()
    setActivePinia(pinia)
    router = createRouter({ history: createMemoryHistory(), routes })
    router.push('/nodes')
    await router.isReady()
  })

  /**
   * 这是本切片的核心断言：面板执行与点按钮**落在同一条路径上**。
   *
   * 不去 spy 内部函数——那种测试换个实现就红，且证明不了行为。
   * 这里只看可观察后果：两条路径产生的 opLog 记录与请求路径必须一致。
   * 面板要是自己发了请求绕过 store，opLog 就只会有一条，对不上。
   */
  it('面板执行与点按钮走同一条路径', async () => {
    const s = await ready()

    const view = mount(NodesView, mountOpts())
    await view.vm.$nextTick()
    const probeBtn = view
      .findAll('[data-node="node-hk-01"] button')
      .find((b) => b.text().includes('探活'))!
    expect(probeBtn).toBeTruthy()
    await probeBtn.trigger('click')
    await new Promise((r) => setTimeout(r))

    expect(s.opLog).toHaveLength(1)
    const fromButton = { ...s.opLog[0] }

    const palette = await openPalette()
    await palette.find('input').setValue('probe node-hk-01')
    await palette.find('input').trigger('keydown', { key: 'Enter' })
    await new Promise((r) => setTimeout(r))

    expect(s.opLog).toHaveLength(2)
    expect(s.opLog[0]).toEqual(fromButton)
    expect(posted).toEqual(['/nodes/node-hk-01/probe', '/nodes/node-hk-01/probe'])
  })

  it('↑↓ 移动选中项，Enter 执行选中的那条', async () => {
    const s = await ready()
    const palette = await openPalette()
    const input = palette.find('input')
    await input.setValue('probe')

    // 两台节点，默认选第一台；按一次 ↓ 应落到第二台
    await input.trigger('keydown', { key: 'ArrowDown' })
    await input.trigger('keydown', { key: 'Enter' })
    await new Promise((r) => setTimeout(r))

    expect(s.opLog[0].node).toBe('node-us-01')
  })

  it('⌘K 唤起，Esc 关闭', async () => {
    await ready()
    const palette = await openPalette()
    expect(palette.find('input').exists()).toBe(true)
    await palette.find('input').trigger('keydown', { key: 'Escape' })
    expect(palette.find('input').exists()).toBe(false)
  })

  it('跳转命令真的换路由，不是假装', async () => {
    await ready()
    const palette = await openPalette()
    await palette.find('input').setValue('审计')
    await palette.find('input').trigger('keydown', { key: 'Enter' })
    await until(() => router.currentRoute.value.path === '/audit')
    expect(router.currentRoute.value.path).toBe('/audit')
  })

  // 破坏性命令要在面板里看得出来，否则 ↓↓Enter 一气呵成就把节点摘了。
  it('破坏性命令被标出来', async () => {
    await ready()
    const palette = await openPalette()
    await palette.find('input').setValue('drain node-hk-01')
    expect(palette.find('[data-destructive="true"]').exists()).toBe(true)
  })
})

describe('下线确认', () => {
  // 确认框是全局挂载的独立组件（App.vue 里与命令面板并排），
  // 因为行内按钮和面板都要用它——放进节点页的话，在别的页面敲 drain 就没人拦。
  beforeEach(async () => {
    pinia = createPinia()
    setActivePinia(pinia)
    router = createRouter({ history: createMemoryHistory(), routes })
    router.push('/nodes')
    await router.isReady()
  })

  // 下线是破坏性动作：必须先说清楚会发生什么，再执行。
  // 直接执行的话，一次误点就把节点摘出去了，而它正在承接流量。
  it('点下线先弹确认，未确认前不发请求', async () => {
    await ready()
    const view = mount(NodesView, mountOpts())
    const dlg = mount(DrainConfirm, mountOpts())
    await view.vm.$nextTick()

    const drainBtn = view
      .findAll('[data-node="node-hk-01"] button')
      .find((b) => b.text().includes('下线'))!
    await drainBtn.trigger('click')
    await view.vm.$nextTick()

    expect(posted).toEqual([])
    await dlg.vm.$nextTick()
    const dialog = dlg.find('[data-testid="drain-confirm"]')
    expect(dialog.exists()).toBe(true)
    // 说明它会做什么，而不只是「确定吗？」
    expect(dialog.text()).toContain('node-hk-01')
    expect(dialog.text()).toContain('不再承接')
  })

  it('确认后才真的下线', async () => {
    const s = await ready()
    const view = mount(NodesView, mountOpts())
    const dlg = mount(DrainConfirm, mountOpts())
    await view.vm.$nextTick()

    await view.findAll('[data-node="node-hk-01"] button').find((b) => b.text().includes('下线'))!.trigger('click')
    await dlg.vm.$nextTick()
    await dlg.find('[data-testid="drain-confirm"] [data-action="confirm"]').trigger('click')
    await new Promise((r) => setTimeout(r))

    expect(posted).toEqual(['/nodes/node-hk-01/drain'])
    expect(s.opLog[0]).toMatchObject({ verb: 'drain', node: 'node-hk-01' })
  })

  it('取消则什么都不做', async () => {
    const s = await ready()
    const view = mount(NodesView, mountOpts())
    const dlg = mount(DrainConfirm, mountOpts())
    await view.vm.$nextTick()

    await view.findAll('[data-node="node-hk-01"] button').find((b) => b.text().includes('下线'))!.trigger('click')
    await dlg.vm.$nextTick()
    await dlg.find('[data-testid="drain-confirm"] [data-action="cancel"]').trigger('click')
    await new Promise((r) => setTimeout(r))

    expect(posted).toEqual([])
    expect(s.opLog).toHaveLength(0)
    expect(dlg.find('[data-testid="drain-confirm"]').exists()).toBe(false)
  })

  // 命令面板里的 drain 走同一个确认，不能因为「敲命令的人知道自己在做什么」就跳过。
  it('面板里的 drain 同样要确认', async () => {
    const s = await ready()
    const dlg = mount(DrainConfirm, mountOpts())
    const palette = await openPalette()

    await palette.find('input').setValue('drain node-hk-01')
    await palette.find('input').trigger('keydown', { key: 'Enter' })
    await new Promise((r) => setTimeout(r))

    expect(posted).toEqual([])
    expect(s.opLog).toHaveLength(0)
    await dlg.vm.$nextTick()
    expect(dlg.find('[data-testid="drain-confirm"]').exists()).toBe(true)
  })
})
