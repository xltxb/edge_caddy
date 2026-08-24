import { expect, test, type Page } from '@playwright/test'
import { resetMocks, visit } from './helpers'

/**
 * 解析域名页 —— **它跟系统设置页共用一个 `PUT /settings`**（契约 §11）。
 *
 * 拆成两页之后，同一个接口有了两个调用方，而它们看到的是各自加载那一刻的
 * 快照。这一页要守的第一件事就是**别把不属于自己的字段发回去**。
 */

/** 模拟「别处改了服务商」：另一页保存过，或者另一个人刚改完。 */
async function setKindElsewhere(page: Page, kind: string): Promise<void> {
  const status = await page.evaluate(async (kind) => {
    const r = await fetch('/api/v1/settings', {
      method: 'PUT',
      credentials: 'same-origin',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ dns_provider: { kind } }),
    })
    return r.status
  }, kind)
  expect(status, `把服务商设成 ${kind} 失败`).toBe(200)
}

async function readKind(page: Page): Promise<string> {
  return await page.evaluate(async () => {
    const r = await fetch('/api/v1/settings', { credentials: 'same-origin' })
    return (await r.json())?.data?.dns_provider?.kind as string
  })
}

test.describe('解析域名', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/domains')
    await resetMocks(page)
    await page.reload()
    await page.waitForSelector('.target, .empty-targets')
  })

  /**
   * **这一页保存时只发 `targets`，别的字段一个都不带。**
   *
   * 它防的不是一个假想的场景：两页现在能同时开着，而这一页加载时看到的 `kind`
   * 是那一刻的快照。把它原样发回去，就会把另一页刚改的那个**覆盖掉，
   * 且没有任何一处会说出来** —— 服务商悄悄变回上一家，下一次同步推到错的地方。
   *
   * ## 为什么验的是结论不是 body
   *
   * 断言「PUT body 里没有 kind 这个键」也能红，但它钉的是**当前这个写法**：
   * 换成发 `kind: undefined`、或者发一个跟现值相同的 kind，那条会绿而问题还在。
   *
   * 这里验的是**服务商没被改动**，那是真正要保住的东西 —— 无论用哪种写法达成。
   *
   * ## 少发和多发的代价不对称
   *
   * 契约 §11：「不给 = 不动」。所以少发一个字段最坏是「这次没改到」，
   * 而多发一个字段最坏是「悄悄改回去了」。前者人会发现，后者不会。
   */
  test('保存域名，不会覆盖别处刚改的服务商', async ({ page }) => {
    // 这一页此刻加载到的是 mock 的初始服务商
    const before = await readKind(page)
    expect(before, 'seed 的服务商是空的，这条测的是空集').toBeTruthy()

    // 别处把它改成了另一家 —— 这一页并不知道
    const other = before === 'dnspod' ? 'cloudflare' : 'dnspod'
    await setKindElsewhere(page, other)

    // 这一页只改域名，然后保存
    const first = page.locator('[data-field="domain"]')
    await first.fill('changed.example.com')
    const save = page.locator('header.head button.primary')
    await expect(save, '改了域名而保存按钮没亮').toBeEnabled()
    await save.click()
    await expect(save, '保存没完成').toBeDisabled()

    expect(
      await readKind(page),
      `域名页把别处刚改的服务商从 ${other} 覆盖回 ${before} 了 —— ` +
        `解析会被推到另一家去，而界面上没有任何一处说得出为什么`,
    ).toBe(other)

    // 而它自己要改的那件事确实存进去了 —— 否则「没覆盖」可能只是因为它什么都没发
    await expect(first, '域名没保存成功').toHaveValue('changed.example.com')
  })

  /**
   * **加一行、删一行，都要能存下去。**
   *
   * 删到零条也算 —— 那表达的是「不再管任何域名」，是个合法意图；
   * 拦住它等于逼人去数据库里改。
   */
  test('加一条域名，存得进去；再删掉，也存得回来', async ({ page }) => {
    const rows = page.locator('.target')
    const n0 = await rows.count()
    expect(n0, 'seed 里一条域名都没有，这条测的是空集').toBeGreaterThan(0)

    await page.locator('button.add').click()
    await expect(rows).toHaveCount(n0 + 1)

    // 新行是空的 —— 填上再存，否则存的是一条没有意义的记录
    const last = rows.nth(n0)
    await last.locator('.t-domain').fill('added.example.com')
    await last.locator('.t-sub').fill('www')

    const save = page.locator('header.head button.primary')
    await save.click()
    await expect(save).toBeDisabled()

    // 存完重新拉回来的那一份里要有它
    await expect(rows).toHaveCount(n0 + 1)
    await expect(rows.nth(n0).locator('.t-domain')).toHaveValue('added.example.com')

    // 再删掉
    await rows.nth(n0).locator('button.mini', { hasText: '删除' }).click()
    await expect(rows).toHaveCount(n0)
    await save.click()
    await expect(save).toBeDisabled()
    await expect(rows, '删掉的那条又回来了 —— PUT 大概被当成了逐条合并').toHaveCount(n0)
  })

  /**
   * **没选服务商时，这一页填什么都不生效** —— 而它存得进去。
   *
   * 后端不拦（域名本身合法），所以界面上不说的话，这就是个
   * **存得下、看得见、而完全不起作用的配置**。那比报错难发现得多：
   * 人会以为配好了，然后去查为什么解析没变。
   */
  test('没选服务商时，要说明这里填的不会被推到任何地方', async ({ page }) => {
    await setKindElsewhere(page, '')
    // 走 SPA 路由让这一页重新挂载 —— `reload()` 会把 MSW 的 seed 清回去（见 visit）
    await visit(page, '/domains', '.target, .empty-targets')

    const warn = page.locator('.hint.warn')
    await expect(warn, '没选服务商而这一页什么都没说').toHaveCount(1)
    await expect(warn).toContainText('不会被推到任何地方')
    // 去哪儿改要指出来，否则人得自己找
    await expect(warn.locator('a[href="/settings"]')).toHaveCount(1)
  })
})
