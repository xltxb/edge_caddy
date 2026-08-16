import { defineStore } from 'pinia'
import { computed, reactive, ref } from 'vue'
import { get, post, put } from '@/api/http'

/** AlertView 是后端的对外表示：**没有任何凭据**，只说「配没配」。 */
export interface AlertView {
  enabled: boolean
  min_level: 'all' | 'warn' | 'crit'
  at_all_on_crit: boolean
  max_retries: number
  webhook_configured: boolean
  lark_configured: boolean
  lark_signed: boolean
  sent: number
  failed: number
  dropped: number
}

type Loader = () => Promise<AlertView>
type Saver = (body: unknown) => Promise<AlertView>
type Tester = () => Promise<unknown>

export const useAlertsStore = defineStore('alerts', () => {
  /**
   * form 里的凭据字段**永远从空开始**。
   *
   * 后端不回显明文（读接口里就没有），所以这里也没有可填回去的东西。
   * 留空提交表示「不改动」——把空当成清空的话，改一次通知级别就会顺手把
   * Webhook 抹掉，而界面上一切正常。
   */
  const form = reactive({
    enabled: false,
    min_level: 'warn' as AlertView['min_level'],
    at_all_on_crit: false,
    max_retries: 2,
    webhook_url: '',
    lark_url: '',
    lark_secret: '',
  })

  const webhookConfigured = ref(false)
  const larkConfigured = ref(false)
  const larkSigned = ref(false)
  const stats = ref({ sent: 0, failed: 0, dropped: 0 })
  const loading = ref(false)
  const saving = ref(false)
  const error = ref('')
  const testing = ref(false)
  const testResult = reactive({ ok: false, msg: '' })

  // 清除标记不进 form：它们是一次性的动作，不是设置项。
  const clears = reactive({ webhook: false, lark: false, lark_secret: false })

  let loader: Loader = () => get<AlertView>('/alerts')
  let saver: Saver = (body) => put<AlertView>('/alerts', body)
  let tester: Tester = () => post<unknown>('/alerts/test')

  /** __setIO / __setTester 只供测试替换网络边界。 */
  function __setIO(l: Loader, s: Saver) {
    loader = l
    saver = s
  }
  function __setTester(t: Tester) {
    tester = t
  }

  function apply(v: AlertView) {
    form.enabled = v.enabled
    form.min_level = v.min_level
    form.at_all_on_crit = v.at_all_on_crit
    form.max_retries = v.max_retries
    webhookConfigured.value = v.webhook_configured
    larkConfigured.value = v.lark_configured
    larkSigned.value = v.lark_signed
    stats.value = { sent: v.sent ?? 0, failed: v.failed ?? 0, dropped: v.dropped ?? 0 }
    // 凭据框保存后清空：留着会让人以为它被回显了，下次再点保存就把
    // 同一个值又发一遍
    form.webhook_url = ''
    form.lark_url = ''
    form.lark_secret = ''
    clears.webhook = clears.lark = clears.lark_secret = false
  }

  async function load() {
    loading.value = true
    error.value = ''
    try {
      apply(await loader())
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  }

  /** payload 省掉未填写的凭据字段，让「留空=不改动」在两侧语义一致。 */
  function payload(): Record<string, unknown> {
    const body: Record<string, unknown> = {
      enabled: form.enabled,
      min_level: form.min_level,
      at_all_on_crit: form.at_all_on_crit,
      max_retries: form.max_retries,
    }
    if (form.webhook_url) body.webhook_url = form.webhook_url
    if (form.lark_url) body.lark_url = form.lark_url
    if (form.lark_secret) body.lark_secret = form.lark_secret
    if (clears.webhook) body.clear_webhook = true
    if (clears.lark) body.clear_lark = true
    if (clears.lark_secret) body.clear_lark_secret = true
    return body
  }

  async function save() {
    saving.value = true
    error.value = ''
    try {
      apply(await saver(payload()))
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      saving.value = false
    }
  }

  function clearWebhook() {
    clears.webhook = true
  }
  function clearLark() {
    clears.lark = true
  }
  function clearLarkSecret() {
    clears.lark_secret = true
  }

  async function sendTest() {
    testing.value = true
    testResult.ok = false
    testResult.msg = ''
    try {
      await tester()
      testResult.ok = true
      testResult.msg = '已发出。没收到就说明地址或凭据不对。'
    } catch (e) {
      // 失败原因原样留在界面上：那是配错了时唯一有用的信息
      testResult.ok = false
      testResult.msg = (e as Error).message
    } finally {
      testing.value = false
    }
  }

  const pendingClears = computed(() => clears.webhook || clears.lark || clears.lark_secret)

  return {
    form, webhookConfigured, larkConfigured, larkSigned, stats,
    loading, saving, error, testing, testResult, pendingClears,
    load, save, sendTest, clearWebhook, clearLark, clearLarkSecret,
    __setIO, __setTester,
  }
})
