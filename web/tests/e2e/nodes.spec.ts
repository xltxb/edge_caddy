import { expect, test } from '@playwright/test'
import { resetMocks } from './helpers'

/**
 * 节点的改元数据与删记录。
 *
 * seed 里恰好有两台机器构成这一组最要紧的对照：
 *
 *   `node-de-01` 法兰克福 —— **已下线**（drained_at 非 null），status 仍是 ok
 *   `node-us-01` 洛杉矶   —— **status: down 而没有 drained_at**
 *
 * 后者是「离线但没人下线过它」，也就是删除那道前提唯一能被证伪的场景：
 * 判据要是写成 `status === 'down'`，这台机器会变得可删，而它的隧道随时可能
 * 回来（Agent 的 Restart=always 一直在重连）。
 */

/** 展开某台节点的详情行 —— 操作按钮都在展开之后。 */
async function expand(page: import('@playwright/test').Page, id: string) {
  await page.getByRole('button', { name: new RegExp(id) }).click()
}

test.describe('节点元数据与删除', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/nodes')
    await resetMocks(page)
    await page.reload()
  })

  test('离线但没下线：删除按钮按不动，且说的是「先下线」而不是「你确定吗」', async ({
    page,
  }) => {
    await expand(page, 'node-us-01')
    const del = page.getByRole('button', { name: '删除记录' })

    await expect(del).toBeDisabled()
    /*
     * 措辞是**前提**不是劝阻。写成「不可撤销，确定吗」，人会以为自己在被劝阻，
     * 然后去找地方跳过它 —— 而这一条跳不得：删掉一台还会重连的机器的记录，
     * 它会被按证书认出来，然后在一张不存在的行上写心跳（UPDATE 影响 0 行，
     * 不报错），结果是一台连着、在服务、而控制台上看不见的机器。
     */
    await expect(del).toHaveAttribute('title', /先下线/)
  })

  test('已下线：可以删，而弹层说清「只删了一半」', async ({ page }) => {
    await expand(page, 'node-de-01')
    await page.getByRole('button', { name: '删除记录' }).click()

    const dialog = page.getByRole('dialog', { name: '删除节点' })
    // 删之前先说边界：这一下不碰那台机器
    await expect(dialog).toContainText('Agent 与 Caddy 不受影响')

    await dialog.getByRole('button', { name: '确认删除' }).click()

    /*
     * **另一半必须自己说出来。** 人点完删除会以为干净了，而那台机器还在监听
     * 80/443、还在用最后一次拿到的配置服务。这句 detail 是后端给的原话，
     * 里面那条 uninstall 命令是「另一半」的全部内容。
     */
    await expect(dialog).toContainText('edge-node.sh uninstall')
    await dialog.getByRole('button', { name: '知道了' }).click()

    await expect(page.getByRole('button', { name: /node-de-01/ })).toHaveCount(0)
  })

  test('只改城市：不冒出「解析已同步」—— 那不是失败，是跟解析无关', async ({ page }) => {
    await expand(page, 'node-hk-01')
    await page.getByRole('button', { name: '修改信息' }).click()

    const dialog = page.getByRole('dialog', { name: '修改节点' })
    await dialog.getByLabel('城市').fill('香港（沙田）')
    await dialog.getByRole('button', { name: '保存' }).click()

    await expect(dialog).toContainText('不涉及公网 IP')
    /*
     * 这一条守的是**别把「跟解析无关」渲染成一次失败**。dns_synced 为 false
     * 在这里是正常的，用警示色或者说「解析没有变动」都会让人以为出了事。
     */
    await expect(dialog).not.toContainText('解析已同步')
    await dialog.getByRole('button', { name: '知道了' }).click()

    await expect(page.getByText('香港（沙田）')).toBeVisible()
  })

  test('改公网 IP：说出解析真的同步了，并带上新旧值', async ({ page }) => {
    await expand(page, 'node-hk-01')
    await page.getByRole('button', { name: '修改信息' }).click()

    const dialog = page.getByRole('dialog', { name: '修改节点' })
    await dialog.getByLabel('公网 IP').fill('103.117.44.19')
    await dialog.getByRole('button', { name: '保存' }).click()

    // detail 是后端原话，前端不自己编 —— 它带着新旧两个值，那是人核对的依据
    await expect(dialog).toContainText('103.117.44.18')
    await expect(dialog).toContainText('103.117.44.19')
    await expect(dialog).toContainText('解析已同步到服务商')
  })

  test('节点 ID 不在表单里，而且说明了为什么', async ({ page }) => {
    await expand(page, 'node-hk-01')
    await page.getByRole('button', { name: '修改信息' }).click()

    const dialog = page.getByRole('dialog', { name: '修改节点' })
    /*
     * 一个只是「不能改」的灰输入框会让人以为是权限问题，然后去找能改它的人。
     * 说清它是证书里的身份，人才知道该走「删掉再接一台」。
     */
    await expect(dialog).toContainText('隧道证书')
    await expect(dialog.getByLabel('节点 ID')).toHaveCount(0)
  })

  /*
   * 这一条守的是**这个字段唯一的用途**：在别的字段全绿时说话。
   *
   * seed 里 node-jp-01 是那个场景的固定夹具 —— status ok、在线、心跳新鲜、
   * 不漂移，四个字段全是健康的，而它过去一小时断了 4 次。灰度上真发生过
   * （CDN 每十几分钟切一次长连接），当时界面上看不出任何异常。
   *
   * 配一条反面：node-hk-01 的 reconnects_1h 是 0，那一句**不该出现** ——
   * 没有反面的话，一个「永远显示这句」的实现也能让上半条通过。
   */
  test('徽标全绿而隧道在反复断：详情里说得出来，而稳定的节点不说', async ({ page }) => {
    await expand(page, 'node-jp-01')
    const jp = page.locator('li', { hasText: 'node-jp-01' })
    await expect(jp).toContainText('已连接')
    await expect(jp).toContainText('过去 1 小时断连')
    await expect(jp).toContainText('4')

    await expand(page, 'node-hk-01')
    const hk = page.locator('li', { hasText: 'node-hk-01' })
    await expect(hk).toContainText('已连接')
    await expect(hk).not.toContainText('过去 1 小时断连')
  })
})
