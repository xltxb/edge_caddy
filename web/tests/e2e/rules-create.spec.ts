import { expect, test, type Page } from '@playwright/test'
import { resetMocks } from './helpers'

/**
 * **能不能建出一条规则。**
 *
 * ## 这条测的是我漏掉的那件事
 *
 * 七种规则的显示与编辑都做了，唯独没有创建入口 —— 而**在 dev 里看不出来**：
 * mock 的种子预置了各类型各一条，于是打开访问控制页，七种都在、都能编辑，
 * 看起来这一批是完整的。
 *
 * 真实环境里那张表是空的，而没有任何地方能建出第一条。
 * 用户的原话是「后端都已经实现了，前端没看到相应的页面和配置项」。
 *
 * > **种子数据会掩盖入口的缺失。** 验「显示对不对」永远验不出
 * > 「一个空系统能不能走到这个状态」。
 *
 * 所以这一条从**点按钮**开始，不从种子里已有的那几条开始。
 *
 * ## 顺带钉住 mock 那一半
 *
 * `PUT /rules/:id` 从前在 mock 里根本不存在（只有 DELETE）——
 * 「更换密钥」和「新建规则」都走它，请求落到 SPA 的 HTML fallback，
 * **而界面上那两个按钮看起来完全正常**：点下去、转一下、什么也没发生。
 *
 * `check:shapes` 抓不到这一类：写端点它只比两个，其余写明了不比的理由。
 * 而「不比形状」不等于「mock 里有它」。
 */

async function openNew(page: Page): Promise<void> {
  await page.click('header.head button.primary')
  await page.waitForSelector('.modal')
}

test.describe('新建访问规则', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/acl')
    await resetMocks(page)
    await page.reload()
    await page.waitForSelector('table.table')
  })

  /**
   * 完整走一遍：选类型 → 填 ID 与名称 → 创建 → 跳到工作台。
   *
   * 断言落在**结果**上（列表里多了一条、工作台打开的是它），
   * 而不是「按钮点得动」。
   */
  test('建一条限流规则，然后落在它的编辑页上', async ({ page }) => {
    const before = await page.locator('table.table tbody tr').count()
    expect(before, '种子里一条规则都没有，这条测的是空集').toBeGreaterThan(0)

    await openNew(page)
    await page.selectOption('.modal select', 'rate_limit')
    await page.fill('.modal input.mono', 'api-rl')
    await page.fill('.modal input:not(.mono)', 'API 限流')
    await page.click('.modal button[type="submit"]')

    // 建完直接跳工作台 —— 建完就得配，把人留在列表页只会让他再点一次
    await page.waitForURL('**/workbench/**')
    await expect(page.locator('.col-title').first()).toContainText('API 限流')

    /*
     * **回到列表，那条要在。**
     *
     * 只验「跳转了」不够：跳转发生在 `createRule` 的 await 之后，
     * 而那个 await 就算什么也没存进去也会正常返回（mock 少一个 handler 时
     * 请求会落到 SPA fallback —— HTTP 200，一份 HTML）。
     */
    await page.click('nav a[href="/acl"]')
    await page.waitForSelector('table.table')
    const row = page.locator('table.table tbody tr', { hasText: 'api-rl' })
    await expect(row, '列表里没有刚建的那条 —— 它多半没真的存进去').toHaveCount(1)
    await expect(row).toContainText('限流')
    // 骨架给的是后端认得的合法值，不是「随便一个数」
    await expect(row).toContainText('每 60 秒 100 次')
  })

  /**
   * **新建出来的是停用、未绑定的**，而界面必须这么显示。
   *
   * 后端的校验只跑在启用且已绑定的规则上，所以骨架存得进去。
   * 但人要知道它还没在工作 —— 否则他建完就走，以为限流已经生效了。
   */
  test('新建的规则是停用的，列表上要显示成不生效', async ({ page }) => {
    await openNew(page)
    await page.selectOption('.modal select', 'geo_block')
    await page.fill('.modal input.mono', 'geo-test')
    await page.fill('.modal input:not(.mono)', '地域测试')
    await page.click('.modal button[type="submit"]')
    await page.waitForURL('**/workbench/**')

    await page.click('nav a[href="/acl"]')
    await page.waitForSelector('table.table')
    const row = page.locator('table.table tbody tr', { hasText: 'geo-test' })
    await expect(row, '新建的规则显示成生效中 —— 人会以为它已经在拦人了').toContainText(
      '已停用',
    )
    await expect(row).toContainText('未绑定域名')
  })

  /**
   * **重名必须在前端拦住，因为后端不会拒。**
   *
   * `PUT /rules/:id` 是 upsert：填一个已存在的 id，它会把那条规则整个换掉
   * 并回 `code: 0`。这跟路由不同（`POST /routes` 重名回 `1004`）。
   *
   * 所以这一条不是「重名不好看」，是**一次静默的覆盖** —— 人以为自己新建了
   * 一条，实际把别人配好的那条抹了，而两边都没有任何提示。
   */
  test('用已有的 ID：拦住，并说清后果是覆盖', async ({ page }) => {
    await openNew(page)
    // 种子里已有的一条
    await page.fill('.modal input.mono', 'office-wl')
    await page.fill('.modal input:not(.mono)', '随便一个名字')

    const err = page.locator('.modal .err')
    await expect(err, '重名没被拦下 —— 保存会静默覆盖掉已有的那条').toHaveCount(1)
    await expect(err, '只说了「已存在」，没说后果是覆盖').toContainText('覆盖')
    await expect(page.locator('.modal button[type="submit"]')).toBeDisabled()
  })

  /**
   * **空态是最需要那个入口的一屏。**
   *
   * 用户撞到的正是这一屏：真实环境里规则表是空的。
   * 而它从前只有一句「还没有访问规则」—— 一句陈述句读起来像
   * 「这里没有东西」，不像「这里可以有东西」。
   */
  test('一条规则都没有时，那一屏上要有建第一条的入口', async ({ page }) => {
    // 把种子里的规则全删掉 —— 造出用户看到的那一屏
    const ids = await page.evaluate(async () => {
      const r = await fetch('/api/v1/rules', { credentials: 'same-origin' })
      const items = (await r.json())?.data?.items as { id: string }[]
      for (const it of items) {
        await fetch(`/api/v1/rules/${it.id}`, { method: 'DELETE', credentials: 'same-origin' })
      }
      return items.map((i) => i.id)
    })
    expect(ids.length, '种子里就没有规则，这条测的是空集').toBeGreaterThan(0)

    /*
     * **这里要整页重载，不能只在 SPA 里绕一圈。**
     *
     * 上面那几个 DELETE 是直接发的，绕过了 store —— 而 `AclView` 的 onMounted
     * 是 `if (!config.rules.length) fetchAll()`：store 里还留着旧的那 8 条，
     * 于是它不去重取，页面照旧显示一张满的表。
     *
     * `reload()` 在这里是安全的：规则由 **Node 侧的 config-mock** 提供，
     * 那份 state 活在 Node 进程里，整页导航不会把它重置
     * （会被重置的是 MSW 的 seed —— 见 `helpers.visit`）。
     */
    await page.reload()
    await page.waitForSelector('.hint')

    await expect(page.locator('table.table'), '规则应该已经删光了').toHaveCount(0)
    await expect(
      page.locator('.hint button'),
      '空态上没有建第一条的入口 —— 而这正是真实环境打开时看到的那一屏',
    ).toHaveCount(1)
  })
})
