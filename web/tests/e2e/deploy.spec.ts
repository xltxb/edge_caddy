import { expect, test } from '@playwright/test'
import { pendingBadge, resetMocks, withDraftSaved } from './helpers'

/**
 * 下发流水线 —— 前端开发文档 §7 要求必须有 e2e 的两条关键流程之一。
 *
 * 这条链路横跨草稿、预览、权威 diff、确认、逐节点进度（经真 WebSocket）
 * 与落定后的状态回收。单测覆盖得了其中每一段，但覆盖不了它们接在一起。
 */
test.describe('配置下发', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/workbench')
    await resetMocks(page)
    await page.reload()
    await expect(page.getByText('反代路由').first()).toBeVisible()
  })

  /*
   * 名字说「**从改动**到落定」，那就得真的从一次改动开始。
   *
   * 第一版是从 seed 预置的草稿直接点下发 —— 编辑那一段一次都没走过。
   * 从名字推改坏立刻露馅：把「改字段 → 写草稿」那条路弄断，这条测试照样绿，
   * 而它的名字承诺覆盖了那一段。**名字讲的事测试够不着，比断言写错更难发现**，
   * 因为断言会被执行，名字不会。
   */
  test('从改动到落定的完整链路', async ({ page }) => {
    // seed 预置了两条草稿、共 3 处改动
    await expect(pendingBadge(page)).toHaveText('3 处变更')

    // 真的改一处 —— 这一段是名字里「从改动」那三个字
    await page.goto('/workbench/route:api.example.com')
    await withDraftSaved(page, () => page.getByLabel('回源地址').fill('10.8.0.44:8080'))
    await expect(pendingBadge(page)).toHaveText('4 处变更')

    await expect(page.getByRole('button', { name: /校验并下发/ })).toBeEnabled()
    await page.getByRole('button', { name: /校验并下发/ }).click()

    const modal = page.getByRole('dialog', { name: '校验并下发' })
    await expect(modal).toBeVisible()

    // 预览时不编新版本号 —— 新号在下发那一刻才生成
    await expect(modal).toContainText('新版本（下发时生成）')
    await expect(modal).not.toContainText(/→ cfg-[0-9a-f]{6}$/)

    // 证书段不在 diff 里，这句话不标就是给一个兑现不了的承诺
    await expect(modal).toContainText('证书段由主控自动附加，不在此 diff 中')

    // 权威 diff 默认折叠，展开后才出现
    await expect(modal.getByText('⋯', { exact: false })).toHaveCount(0)
    await modal.getByRole('button', { name: /查看完整变更/ }).click()
    await expect(modal.getByText(/行未变更，点击展开/).first()).toBeVisible()

    await modal.getByRole('button', { name: /确认下发到 \d+ 个节点/ }).click()

    // 逐节点进度：不允许「整体成功/失败」黑盒（PRD §7）
    await expect(modal.getByText(/热重载进度/)).toBeVisible()
    await expect(modal.getByText('node-hk-01')).toBeVisible()

    // 落定。mock 里 node-tw-01 会重试一次后成功、node-us-01 放弃重试
    await expect(modal.getByText(/个节点已接受配置/)).toBeVisible({ timeout: 20_000 })

    // 「已接受」不等于「已生效」—— Caddy 收下配置不代表流量在走
    await expect(modal).toContainText('不代表流量已经在走')

    await modal.getByRole('button', { name: '关闭' }).click()

    // 下发过的草稿被消费掉
    await expect(pendingBadge(page)).toHaveText('无')
    await expect(page.getByRole('button', { name: /校验并下发/ })).toBeDisabled()
  })

  test('放弃重试的节点标为需人工处理，重试中的不标', async ({ page }) => {
    await page.getByRole('button', { name: /校验并下发/ }).click()
    const modal = page.getByRole('dialog', { name: '校验并下发' })
    await modal.getByRole('button', { name: /确认下发到 \d+ 个节点/ }).click()

    // 重试期间：那一行还会再动，不能标成终态
    const retrying = modal.locator('li.row').filter({ hasText: '重试中' })
    await expect(retrying.first()).toBeVisible({ timeout: 15_000 })

    /*
     * 名字里「**重试中的不标**」那一半。
     *
     * 第一版只断言「重试中」出现，没断言那一刻「需人工处理」不在**同一行**上 ——
     * 界面要是两个都标，这条测试照样绿。而那正是这句措辞要防的误报：一个还会
     * 自己好起来的节点被标成需要人去处理，人半夜爬起来看一眼，它已经好了。
     */
    /*
     * **一次性读取那一行的文本**，而不是写一条 `toHaveCount(0)`。
     *
     * 第一版写的是 `retrying.filter({ hasText: '需人工处理' }).toHaveCount(0)`，
     * 改坏之后它照样绿 —— 因为 Playwright 的匹配器会重试到超时，而那个节点重试
     * 成功之后行里就没有「重试中」了，被过滤的集合变空，断言于是成立。
     *
     * **它是被「对象消失」满足的，不是被「对象正确」满足的。** 这是否定断言那一族
     * 的第三种变体：前两种是装置没打开、断言比意图宽，这一种是**等到条件不再适用**。
     * 一次性读取没有这个后门：行没了就抛，不会静静地变绿。
     */
    const rowText = await retrying.first().innerText()
    expect(rowText).toContain('重试中')
    expect(rowText).not.toContain('需人工处理')

    // 重试用尽后转终态。ADR-0005：Caddy 拒绝的不重试，传输层失败才重试
    await expect(modal.getByText('需人工处理')).toBeVisible({ timeout: 20_000 })
  })

  test('下发后基线推进，资源版本 +1', async ({ page }) => {
    await page.getByRole('button', { name: /校验并下发/ }).click()
    const modal = page.getByRole('dialog', { name: '校验并下发' })
    await modal.getByRole('button', { name: /确认下发到 \d+ 个节点/ }).click()
    await expect(modal.getByText(/个节点已接受配置/)).toBeVisible({ timeout: 20_000 })
    await modal.getByRole('button', { name: '关闭' }).click()

    // seed 里 api.example.com 是 v7，下发后应为 v8
    await page.goto('/routes')
    const row = page.getByRole('row').filter({ hasText: 'api.example.com' })
    await expect(row).toContainText('v8')
    // 草稿里改的 10MB 现在是 live 值
    await expect(row).toContainText('5 条')
  })
})
