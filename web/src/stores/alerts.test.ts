import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useAlertsStore, type AlertView } from './alerts'

const view: AlertView = {
  enabled: true, min_level: 'warn', at_all_on_crit: true, max_retries: 2,
  webhook_configured: true, lark_configured: false, lark_signed: false,
  sent: 12, failed: 1, dropped: 0,
}

describe('告警设置 store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('载入后反映「配没配」，而不是凭据本身', async () => {
    const s = useAlertsStore()
    s.__setIO(async () => view, async () => view)
    await s.load()

    expect(s.form.enabled).toBe(true)
    expect(s.form.min_level).toBe('warn')
    expect(s.webhookConfigured).toBe(true)
    expect(s.larkConfigured).toBe(false)
    // 表单里的凭据框永远是空的：后端根本不给明文
    expect(s.form.webhook_url).toBe('')
    expect(s.form.lark_secret).toBe('')
  })

  // 提交时不带空凭据字段。
  //
  // 带上空串的话，后端那边「留空=不改动」的约定就得靠后端自己兜；
  // 前端把空字段直接省掉，语义在两侧都是一致的。
  it('提交时省略未填写的凭据字段', async () => {
    const s = useAlertsStore()
    let sentBody: Record<string, unknown> = {}
    s.__setIO(async () => view, async (body) => {
      sentBody = body as Record<string, unknown>
      return view
    })
    await s.load()
    s.form.min_level = 'crit'
    await s.save()

    expect(sentBody.min_level).toBe('crit')
    expect('webhook_url' in sentBody).toBe(false)
    expect('lark_secret' in sentBody).toBe(false)
  })

  it('填了凭据才带上', async () => {
    const s = useAlertsStore()
    let sentBody: Record<string, unknown> = {}
    s.__setIO(async () => view, async (body) => {
      sentBody = body as Record<string, unknown>
      return view
    })
    await s.load()
    s.form.webhook_url = 'https://hooks.example.com/a/b/c'
    await s.save()

    expect(sentBody.webhook_url).toBe('https://hooks.example.com/a/b/c')
  })

  // 清空是显式动作，不是「把框清掉再保存」。
  it('清除凭据用显式标记', async () => {
    const s = useAlertsStore()
    let sentBody: Record<string, unknown> = {}
    s.__setIO(async () => view, async (body) => {
      sentBody = body as Record<string, unknown>
      return view
    })
    await s.load()
    s.clearWebhook()
    await s.save()

    expect(sentBody.clear_webhook).toBe(true)
  })

  // 保存后表单里的凭据框要清掉：留着会让人以为它被回显了，
  // 下次再点保存就把同一个值又发了一遍。
  it('保存成功后清掉凭据输入框', async () => {
    const s = useAlertsStore()
    s.__setIO(async () => view, async () => view)
    await s.load()
    s.form.webhook_url = 'https://hooks.example.com/a/b/c'
    await s.save()

    expect(s.form.webhook_url).toBe('')
  })

  // 投递失败计数要露出来。静默失败的告警系统比没有告警更糟——
  // 人以为自己被保护着。
  it('展示投递计数', async () => {
    const s = useAlertsStore()
    s.__setIO(async () => view, async () => view)
    await s.load()
    expect(s.stats).toEqual({ sent: 12, failed: 1, dropped: 0 })
  })

  it('测试发送的失败原因要留在界面上', async () => {
    const s = useAlertsStore()
    s.__setIO(async () => view, async () => view)
    s.__setTester(async () => {
      throw new Error('测试发送失败：lark: 对端返回 HTTP 500')
    })
    await s.load()
    await s.sendTest()

    expect(s.testResult.ok).toBe(false)
    expect(s.testResult.msg).toContain('HTTP 500')
  })
})
