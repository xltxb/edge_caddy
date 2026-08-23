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
})
