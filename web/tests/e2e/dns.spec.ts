import { expect, test } from '@playwright/test'
import { resetMocks } from './helpers'

/**
 * DNS 调度页：**同步成功时那句 detail 不能被丢掉。**
 *
 * ## 这一条守的是什么
 *
 * 界面此前在 `dns_sync.ok` 时只显示时间（「上次同步到服务商：01:11:30」），
 * 而后端在成功的 detail 里说的是**实际发生了什么**：记录写到了哪个名字、
 * 这次按什么口径推的。
 *
 * 那个名字是唯一能揭穿这种错的东西：`domain` 填 `cdn.example.com` 而 `sub`
 * 又填 `cdn`，记录会建到 `cdn.cdn.example.com` —— **推送成功、接口 200、
 * `ok` 是 true，而人在服务商面板上永远找不到它**。服务商也不会拦，那是它 zone
 * 里一个合法的子域名。
 *
 * 「成功」两个字本身信息量为零。**丢掉 detail 等于把那道唯一的检查关掉** ——
 * 而它此前确实被丢掉了，是后端提了一嘴才发现的。
 *
 * ## 这一条钉不到什么
 *
 * **它钉的是「界面把那句话显示出来了」，不是「那句话说的是实情」。**
 *
 * detail 是一段自由文本，前端无从验证记录到底写到了哪儿 —— 那要去问服务商。
 * 所以这里断言的是**管道通着**：后端说了什么，人就能看到什么。
 * 说的对不对是后端那一侧的事。
 *
 * 这个限度要写出来，否则下一个人会以为「这条绿着 = 那个名字是对的」。
 */
/**
 * 造一个同步场景。**`page.route` 拦不到 `/dns/weights`** —— 它由 Node 侧的
 * vite 插件提供，请求根本不经过 playwright 的路由层（实测拦截次数为 0）。
 * 所以走 mock 自己的注入端点，跟 `__test/reset` 同类。
 *
 * `beforeEach` 里的 `resetMocks` 会把它清掉，用例之间不串。
 */
async function setSync(page: import('@playwright/test').Page, sync: unknown) {
  const res = await page.request.post('/api/v1/__test/dns-sync', { data: sync })
  if (!res.ok()) throw new Error(`注入同步场景失败：HTTP ${res.status()}`)
}

test.describe('DNS 同步状态', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/dns')
    await resetMocks(page)
    await page.reload()
  })

  test('同步成功时，后端那句 detail 要显示出来，不能只剩一个时间', async ({ page }) => {
    const banner = page.locator('.banner').filter({ hasText: '上次同步到服务商' })
    await expect(banner).toHaveCount(1)

    /*
     * 先证明有东西可测：mock 的 detail 非空。它要是空的，下面那条断言
     * 会因为「没什么可显示」而毫无意义地通过。
     */
    const detail = await page.evaluate(async () => {
      const r = await fetch('/api/v1/dns/weights', { credentials: 'same-origin' })
      return (await r.json())?.data?.dns_sync?.detail as string | undefined
    })
    expect(detail, 'mock 的 dns_sync.detail 是空的，这条测的是空集').toBeTruthy()

    // 横幅里要有 detail 的实质内容，不只是时间
    await expect(banner).toContainText(detail!)
  })

  /*
   * **一个布尔说不出「三个里哪一个没上」**（契约 §11）。
   *
   * `ok` 是「全都成功」，三个坏一个时它就是 false —— 而**另外两个是好的这件事，
   * 只有 `targets` 说得出来**。人会按那个布尔决定要不要去查，而「全坏」和
   * 「坏一个」要做的事完全不同。
   *
   * 这个形状用路由拦截造，不改夹具：**mock 的 dns_sync 是全成功的**，那是
   * 上面那条测试的前提。一个夹具不能同时是两种场景，而两个场景都得有人走 ——
   * 我一开始把夹具改成坏的，当场把上面那条打红了。
   */
  test('三个域名坏一个：要列出是哪一个，而不是只说一句失败', async ({ page }) => {
    await setSync(page, {
      ok: false,
      at: new Date().toISOString(),
      detail: '3 个域名里 2 个同步成功；失败的：other.com',
      targets: [
        { hostname: 'cdn.example.com', ok: true, detail: '已同步' },
        { hostname: 'edge.example.com', ok: true, detail: '已同步' },
        { hostname: 'other.com', ok: false, detail: '10000 Authentication error' },
      ],
    })
    await page.reload()

    const banner = page.locator('.banner').filter({ hasText: '待生效的安排' })
    await expect(banner).toHaveCount(1)

    // 三个都要列出来 —— 好的那两个也要，那正是这一列存在的理由
    for (const h of ['cdn.example.com', 'edge.example.com', 'other.com']) {
      await expect(banner, `${h} 没被列出来`).toContainText(h)
    }
    // 而坏的那一个的原因要原样带着
    await expect(banner).toContainText('Authentication error')
  })

  /*
   * `targets` 为 `null` 是**旧数据**（写于只支持单域名的版本），
   * **不是「一个目标都没有」** —— 这一档只显示 detail，不能渲染成一张空列表：
   * 空列表读起来像「一个域名都没配」。
   *
   * ## 这一条挡得住什么，我撞过之后才知道
   *
   * 把 `v-if="sync.targets?.length"` 改成 `v-if="sync.targets"` —— **它照样绿**。
   * 两者在 `null` 上行为一样，那个改坏不是改坏。
   *
   * 它真正挡的是**无条件渲染那个 `<ul>`**（撞过，红）。也就是说它守的是
   * 「这一档不该出现列表」这个结论，而不是某一种写法。
   *
   * 写出来是因为：一条测试**挡得住什么**，跟它的名字听起来挡得住什么，是两回事。
   * 不撞一次不知道 —— 而我第一次撞的那个改法，恰好是它管不着的。
   */
  test('targets 为 null（旧数据）：只显示 detail，不渲染空列表', async ({ page }) => {
    await setSync(page, {
      ok: false,
      at: new Date().toISOString(),
      detail: '同步失败了',
      targets: null,
    })
    await page.reload()

    const banner = page.locator('.banner').filter({ hasText: '待生效的安排' })
    await expect(banner).toContainText('同步失败了')
    await expect(banner.locator('.sync-targets'), 'targets 为 null 时不该有列表').toHaveCount(0)
  })
})
