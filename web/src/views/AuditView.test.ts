import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AuditView from './AuditView.vue'

/**
 * **审计页说的话，必须是它真的知道的事。**
 *
 * 这一页原先自称「全部写操作与登录记录」而只渲染第一页（50 条），
 * `next_before_id` 在 `types.ts` 里声明过之后全仓库没有任何一处读它。
 * 失败登录那条横幅也只在这一页里数（issue #49）。
 *
 * 后果不是少看几行：正常使用一段时间后第一页被日常写操作填满，
 * 暴力破解留下的失败登录被挤出第一页 —— **横幅归零，而页面仍自称「全部」**。
 * 那是这一页唯一一类来自外部的信号，也是 ADR-0013 那三条腿里的一条。
 *
 * 「查不到那次操作」和「那次操作没发生」在界面上长得一模一样。
 */

function row(id: number, over: Partial<Record<string, unknown>> = {}) {
  return {
    id,
    at: '2026-09-13T10:00:00Z',
    operator: 'abiu',
    action: '下发配置',
    target: 'route:a.example.com',
    src_ip: '203.0.113.7',
    result: 'ok',
    detail: '',
    ...over,
  }
}

function envelope(data: unknown): Response {
  return new Response(JSON.stringify({ code: 0, msg: '', data }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

/** 两页：第一页全是日常写操作，第二页里躺着失败登录。 */
function twoPages() {
  const first = { items: [row(200), row(199)], next_before_id: 199 }
  const second = {
    items: [row(3, { action: '登录', result: 'fail', src_ip: '198.51.100.9', operator: 'root' })],
    next_before_id: null,
  }
  return vi.fn(async (url: string) => envelope(url.includes('before_id') ? second : first))
}

async function flush() {
  await new Promise((r) => setTimeout(r, 0))
}

describe('审计页的措辞与它拿到的数据', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('没到底时不自称「全部」，并给得出继续看的入口', async () => {
    vi.stubGlobal('fetch', twoPages())
    const w = mount(AuditView)
    await flush()

    expect(w.text()).not.toContain('全部写操作与登录记录')
    // 还有下一页，就要有一个能往下看的东西——否则「只显示了一部分」
    // 这件事没有任何地方说得出来。
    expect(w.find('[data-test="load-more"]').exists()).toBe(true)
  })

  it('加载更多之后，第二页里的失败登录会被数进来', async () => {
    vi.stubGlobal('fetch', twoPages())
    const w = mount(AuditView)
    await flush()

    // 第一页里没有失败登录，横幅不该出现。
    expect(w.find('[data-test="failed-logins"]').exists()).toBe(false)

    await w.find('[data-test="load-more"]').trigger('click')
    await flush()

    const banner = w.find('[data-test="failed-logins"]')
    expect(banner.exists()).toBe(true)
    expect(banner.text()).toContain('198.51.100.9')
  })

  it('到底之后不再给「加载更多」，措辞也不再含糊', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => envelope({ items: [row(1)], next_before_id: null })),
    )
    const w = mount(AuditView)
    await flush()

    expect(w.find('[data-test="load-more"]').exists()).toBe(false)
    // 到底了才配说「全部」。
    expect(w.text()).toContain('全部')
  })
})
