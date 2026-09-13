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

  it('快速切换筛选时，先发的请求后返回不会盖掉后发的', async () => {
    // 第一次请求慢，第二次快 —— 这正是切换筛选时的常态（不同的 operator
    // 命中不同的索引），而 http 的 AbortSignal 形参五处声明零处传入，
    // 于是先发的那次返回时把后发的结果覆盖掉（issue #76）。
    //
    // 症状是筛选条显示 A 而表格是 B 的数据，**且不会自愈**。
    let call = 0
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        call++
        const slow = call === 1
        await new Promise((r) => setTimeout(r, slow ? 60 : 0))
        if (init?.signal?.aborted) throw new DOMException('aborted', 'AbortError')
        return envelope({
          items: [row(slow ? 1 : 2, { operator: url.includes('bob') ? 'bob' : 'abiu' })],
          next_before_id: null,
        })
      }),
    )

    const w = mount(AuditView)
    await flush()
    // 切到 bob：第二次请求会先回来。
    ;(w.vm as unknown as { operator: string }).operator = 'bob'
    await new Promise((r) => setTimeout(r, 120))

    expect(w.text()).toContain('bob')
    expect(w.text()).not.toContain('abiu')
  })

  it('加载更多之后切筛选，旧页不会混进新筛选的结果', async () => {
    // loadMore 用的是一次性 AbortController，不在 inflight 里——切筛选时
    // `load()` 的 `inflight?.abort()` 掐不到它（issue #76 的修复只覆盖了
    // load vs load 那条路）。它返回后把**旧筛选**的旧页 append 进新筛选的
    // 结果，表格里于是混着两个 operator。
    let call = 0
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        call++
        // 第二次请求（loadMore）很慢，第三次（切筛选后的 load）很快。
        await new Promise((r) => setTimeout(r, call === 2 ? 80 : 0))
        if (init?.signal?.aborted) throw new DOMException('aborted', 'AbortError')
        const who = url.includes('bob') ? 'bob' : 'abiu'
        return envelope({
          items: [row(call, { operator: who })],
          next_before_id: call === 1 ? 99 : null,
        })
      }),
    )

    const w = mount(AuditView)
    await flush()
    await w.find('[data-test="load-more"]').trigger('click')
    // 不等它回来就切筛选。
    ;(w.vm as unknown as { operator: string }).operator = 'bob'
    await new Promise((r) => setTimeout(r, 200))

    // **断言落在表格行上，不是整页文本。** 操作人下拉框里还留着上一次
    // 筛选算出来的 'abiu' 选项，用 w.text() 会撞上它——那是选项，不是数据。
    const rows = w.findAll('tbody tr').map((r) => r.text())
    expect(rows.some((r) => r.includes('abiu'))).toBe(false)
    expect(rows.some((r) => r.includes('bob'))).toBe(true)
  })

  it('筛选着的时候不说「全部」—— 那时看到的是一个人的记录', async () => {
    // 筛了操作人再翻到底，副标题原先照样说「全部写操作与登录记录（N 条）」。
    // 那句话在此刻是假的：N 是 bob 的条数，而页面自称的是全景。
    //
    // 审计页的措辞是这一页唯一的可信度来源 —— 「查不到那次操作」和「那次
    // 操作没发生」在界面上长得一模一样，全靠这句话区分。
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        envelope({
          items: [row(1, { operator: url.includes('bob') ? 'bob' : 'abiu' })],
          next_before_id: null,
        }),
      ),
    )
    const w = mount(AuditView)
    await flush()
    ;(w.vm as unknown as { operator: string }).operator = 'bob'
    await flush()
    await flush()

    const caption = w.find('[data-test="audit-scope"]').text()
    expect(caption).not.toContain('全部写操作与登录记录')
    // 正向的一半：它得说清这是**谁**的记录，否则「不说全部」可以靠
    // 把整句话删掉来满足。
    expect(caption).toContain('bob')
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
