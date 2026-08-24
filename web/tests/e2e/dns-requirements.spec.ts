import { expect, test, type Page } from '@playwright/test'
import { resetMocks, visit } from './helpers'

/**
 * **后端说要填的每个字段，界面上都得有一个能填的框。**
 *
 * 2026-08-23 灰度上撞的那个 bug：`account_id` 被误关在 `cfMode === 'global_key'`
 * 分支里，于是 **api_token 模式下那个框根本不渲染**。人不是「看到一个标着可空
 * 的框然后留空了」，是**填不了它**。症状要绕两层才看得懂：
 *
 *     GET /accounts//load_balancers/pools  →  7003 Could not route to ...
 *
 * 双斜杠就是空字段，而 Cloudflare 的错误消息不认识我们的字段名。
 *
 * ## 这条守的不是「有没有标必填」
 *
 * 红星解决的是「人不知道要填」。而这一条守的是**人有没有地方填** ——
 * 一个红星标在不存在的框上等于没标。
 *
 * ## 为什么它不是第二份知识
 *
 * 必填清单从 `GET /settings` 的 `dns_provider_requirements` 读，**不在这里
 * 硬编码**。那份由后端的 `store.MissingFields` 对空配置求值得出（不是抄本），
 * 所以「哪家要什么」只有一个来源。前端的 `v-if` 仍然由前端写，
 * 而这条测试守着它跟那个来源一致。
 *
 * 字段名与输入框之间**没有映射表**：每个框上标着 `data-field="<PUT body 键名>"`，
 * 而 requirements 的键名与 PUT body 一模一样。加一层 id↔字段名 的对照表，
 * 那张表自己就会变成第三份知识。
 *
 * ## 为什么它现在要跨页找
 *
 * `domain` / `zone_id` 搬到了「解析域名」页，`credential` / `account_id` /
 * `email` 留在系统设置。**必填清单不分页 —— 它说的是「这家服务商要这些东西」，
 * 而那件事跟界面怎么分栏目无关。**
 *
 * 这条测试原先只看 `/settings` 一页。搬走那两个字段的时候：
 *
 * - 正面这条**当场变红**（`domain` 在这一页找不到了）—— 它做了该做的事。
 * - 反面那条**继续绿着，而它已经什么都不验了**：它检查「dnspod 下不该出现
 *   `zone_id`」，而 `zone_id` 从此在那一页永远不存在 —— 断言恒真。
 *
 * 一条测试跟着代码搬家而没跟着改判据，**红的那条会喊，绿的那条不会**。
 * 所以两条都改成在整个界面上找，而不是在某一页上找。
 *
 * ## 这条链上有一段没有机械保障
 *
 * e2e 跑在 mock 上，而 mock 的 requirements 是照契约手抄进 seed 的。
 * `check:shapes` 比 mock 与真主控的**形状**、不比值 —— 抄错了这里不会红。
 * 说出来，不假装它有。
 */

/** 界面上没有 `credential_mode` 选择器的那一档，requirements 里的键是空串。 */
const NO_MODE = ''

/**
 * 必填字段散落的那几页，以及各自「这一页的数据到了」的标志。
 *
 * **加一页忘了加进来，症状是「字段找不到」而不是「这一页没被看」** ——
 * 前者会红，所以这张表漏了会被发现。
 */
const PAGES = [
  { path: '/settings', ready: '#dns-kind' },
  { path: '/domains', ready: '.target, .empty-targets' },
]

/**
 * 把服务商设成某个 kind × mode，**存进去**。
 *
 * 不用界面上的选择器：`#dns-kind` 改的是设置页的本地状态，而「解析域名」页
 * 读的是**已保存的** kind（它决定那一页要不要渲染 Zone ID）。两页要看到同一个
 * 值，只能让它落到服务端那一份上。
 *
 * **在页面里发，不用 `page.request`。** 后者绕过浏览器里的 MSW，改的不是界面
 * 正在消费的那一份。
 */
async function setProvider(page: Page, kind: string, mode: string): Promise<void> {
  const status = await page.evaluate(
    async ([kind, mode]) => {
      const r = await fetch('/api/v1/settings', {
        method: 'PUT',
        credentials: 'same-origin',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ dns_provider: { kind, credential_mode: mode } }),
      })
      return r.status
    },
    [kind, mode],
  )
  expect(status, `把服务商设成 ${kind}/${mode || '(无模式)'} 失败`).toBe(200)
}

/** 走遍所有页面，收集界面上此刻存在的每个 `data-field` 及它能不能编辑。 */
async function fieldsAcrossPages(page: Page): Promise<Map<string, boolean>> {
  const found = new Map<string, boolean>()
  for (const { path, ready } of PAGES) {
    await visit(page, path, ready)
    const boxes = page.locator('[data-field]')
    const n = await boxes.count()
    for (let i = 0; i < n; i++) {
      const el = boxes.nth(i)
      const name = await el.getAttribute('data-field')
      if (name) found.set(name, await el.isEditable())
    }
  }
  return found
}

async function readRequirements(page: Page): Promise<Record<string, Record<string, string[]>>> {
  await page.waitForSelector('#dns-kind')
  const reqs = await page.evaluate(async () => {
    const r = await fetch('/api/v1/settings', { credentials: 'same-origin' })
    return (await r.json())?.data?.dns_provider_requirements as
      | Record<string, Record<string, string[]>>
      | undefined
  })
  /*
   * **先证明有东西可测。** 这一条不是形式：requirements 缺失或为空时，
   * 下面那两层循环一次都不进，而报告跟「全部通过」一模一样。
   */
  expect(reqs, '响应里没有 dns_provider_requirements').toBeTruthy()
  expect(
    Object.keys(reqs!).length,
    'requirements 是空的，下面的循环一次都不会进',
  ).toBeGreaterThan(0)
  return reqs!
}

test.describe('必填字段都得有地方填', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/settings')
    await resetMocks(page)
    await page.reload()
  })

  test('requirements 列出的每个字段，在对应的 kind × mode 下都有输入框', async ({ page }) => {
    const reqs = await readRequirements(page)

    let checked = 0
    for (const [kind, modes] of Object.entries(reqs)) {
      for (const [mode, fields] of Object.entries(modes)) {
        expect(fields.length, `${kind}/${mode || '(无模式)'} 的必填清单是空的`).toBeGreaterThan(0)

        await setProvider(page, kind, mode === NO_MODE ? '' : mode)
        const found = await fieldsAcrossPages(page)

        for (const field of fields) {
          const where = `${kind}${mode ? ` / ${mode}` : ''}`
          expect(
            found.has(field),
            `${where} 要求填 ${field}，而整个界面上没有这个框 —— ` +
              `人填不了它，保存会被拒，或者更糟：存进去一个空值然后在服务商那边炸`,
          ).toBe(true)
          // 存在还不够：藏起来或禁用了同样填不了
          expect(found.get(field), `${where} 的 ${field} 框在但不可编辑`).toBe(true)
          checked += 1
        }
      }
    }

    // 数出来。「0 个字段被检查」和「全都有框」在断言上都不会红
    expect(checked, '一个字段都没检查到').toBeGreaterThan(0)
    console.log(`检查了 ${Object.keys(reqs).length} 家服务商、共 ${checked} 处必填字段`)
  })

  /*
   * 反面：**不该有的也不该有。**
   *
   * 上面那条守「该有的都有」，而一个「把所有框都渲染出来」的实现能让它全绿 ——
   * 那时人会在 dnspod 下看到 Cloudflare 的 Zone ID，或者在 `cloudflare_dns` 下
   * 填一个**根本用不上**的 Account ID（那家的记录挂在 zone 上，账号级权限
   * 用不到）。填一个没用的值不会报错，只会让人以为自己配全了。
   *
   * ## 判据不硬编码字段名
   *
   * 「某个字段出现在**任何** kind 的必填清单里」= 它是一个按服务商区分的字段。
   * 那么在不需要它的 kind × mode 下，它就不该渲染。加第四个 kind 时这条自动覆盖。
   *
   * **它依赖一个假设**：不存在「必填于 A、可选于 B」的字段。当前成立 ——
   * 可选字段（`sub` 子域前缀）不在任何清单里，所以压根不进这个集合。
   * 哪天出现那种字段，这条会红，**而那时该改的是判据不是界面**。
   */
  test('不在这个 kind 必填清单里的按服务商字段，不该渲染', async ({ page }) => {
    const reqs = await readRequirements(page)

    /** 所有 kind 的必填字段并集 —— 「按服务商区分的字段」就是这个集合。 */
    const byProvider = new Set(Object.values(reqs).flatMap((m) => Object.values(m).flat()))
    expect(byProvider.size, '并集是空的，下面一条都不会检查').toBeGreaterThan(0)

    let checked = 0
    for (const [kind, modes] of Object.entries(reqs)) {
      for (const [mode, need] of Object.entries(modes)) {
        await setProvider(page, kind, mode === NO_MODE ? '' : mode)
        const found = await fieldsAcrossPages(page)

        const needed = new Set(need)
        for (const field of byProvider) {
          if (needed.has(field)) continue
          expect(
            found.has(field),
            `${kind}${mode ? ` / ${mode}` : ''} 不需要 ${field}，而界面上有这个框 —— ` +
              `人会填一个用不上的值，而那不会报错`,
          ).toBe(false)
          checked += 1
        }
      }
    }
    expect(checked, '一个「不该有」都没检查到').toBeGreaterThan(0)
    console.log(`检查了 ${checked} 处「不该出现的框」`)
  })
})
