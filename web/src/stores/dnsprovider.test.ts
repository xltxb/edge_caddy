import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useDnsProviderStore, type ProviderView } from './dnsprovider'

const view: ProviderView = {
  kind: 'dnspod',
  dnspod_configured: true,
  cloudflare_configured: false,
  acme_email: 'ops@example.com',
  acme_directory: 'https://acme-staging-v02.api.letsencrypt.org/directory',
  staging: true,
}

describe('DNS 服务商设置 store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('载入后只反映「配没配」，表单里的凭据框是空的', async () => {
    const s = useDnsProviderStore()
    s.__setIO(async () => view, async () => view)
    await s.load()

    expect(s.form.kind).toBe('dnspod')
    expect(s.dnspodConfigured).toBe(true)
    expect(s.form.dnspod_token).toBe('')
    expect(s.form.cloudflare_token).toBe('')
    // 邮箱不是凭据，可以回显——它是确认「配的是哪个账号」的唯一线索
    expect(s.form.acme_email).toBe('ops@example.com')
  })

  // staging 必须显眼：那上面签出来的证书浏览器不认，
  // 而「证书装好了但浏览器还是报警告」查起来很费时。
  it('staging 状态可见', async () => {
    const s = useDnsProviderStore()
    s.__setIO(async () => view, async () => view)
    await s.load()
    expect(s.staging).toBe(true)
    expect(s.stagingHint()).toContain('浏览器')
  })

  it('提交时省略未填写的凭据', async () => {
    const s = useDnsProviderStore()
    let sent: Record<string, unknown> = {}
    s.__setIO(async () => view, async (body) => {
      sent = body as Record<string, unknown>
      return view
    })
    await s.load()
    s.form.acme_email = 'new@example.com'
    await s.save()

    expect(sent.acme_email).toBe('new@example.com')
    expect('dnspod_token' in sent).toBe(false)
    expect('cloudflare_token' in sent).toBe(false)
  })

  it('清除凭据用显式标记', async () => {
    const s = useDnsProviderStore()
    let sent: Record<string, unknown> = {}
    s.__setIO(async () => view, async (body) => {
      sent = body as Record<string, unknown>
      return view
    })
    await s.load()
    s.clearDnspod()
    await s.save()
    expect(sent.clear_dnspod).toBe(true)
  })

  it('保存后清掉凭据输入框', async () => {
    const s = useDnsProviderStore()
    s.__setIO(async () => view, async () => view)
    await s.load()
    s.form.dnspod_token = 'new-token'
    await s.save()
    expect(s.form.dnspod_token).toBe('')
  })
})
