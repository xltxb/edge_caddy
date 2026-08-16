import { defineStore } from 'pinia'
import { reactive, ref } from 'vue'
import { get, put } from '@/api/http'

export interface ProviderView {
  kind: string
  dnspod_configured: boolean
  cloudflare_configured: boolean
  acme_email: string
  acme_directory: string
  /** staging 上签出来的证书浏览器不认，必须显眼提示。 */
  staging: boolean
}

type Loader = () => Promise<ProviderView>
type Saver = (body: unknown) => Promise<ProviderView>

export const LE_STAGING = 'https://acme-staging-v02.api.letsencrypt.org/directory'
export const LE_PRODUCTION = 'https://acme-v02.api.letsencrypt.org/directory'

export const useDnsProviderStore = defineStore('dnsprovider', () => {
  /** 凭据字段**永远从空开始**：后端不回显明文，这里也没有可填回去的东西。 */
  const form = reactive({
    kind: '',
    dnspod_id: '',
    dnspod_token: '',
    cloudflare_token: '',
    acme_email: '',
    acme_directory: LE_STAGING,
  })
  const dnspodConfigured = ref(false)
  const cloudflareConfigured = ref(false)
  const staging = ref(true)
  const loading = ref(false)
  const saving = ref(false)
  const error = ref('')
  const clears = reactive({ dnspod: false, cloudflare: false })

  let loader: Loader = () => get<ProviderView>('/dns/provider')
  let saver: Saver = (body) => put<ProviderView>('/dns/provider', body)

  /** __setIO 只供测试替换网络边界。 */
  function __setIO(l: Loader, s: Saver) {
    loader = l
    saver = s
  }

  function apply(v: ProviderView) {
    form.kind = v.kind
    form.acme_email = v.acme_email
    form.acme_directory = v.acme_directory || LE_STAGING
    dnspodConfigured.value = v.dnspod_configured
    cloudflareConfigured.value = v.cloudflare_configured
    staging.value = v.staging
    // 凭据框保存后清空：留着会让人以为它被回显了，下次再点保存就把同一个值
    // 又发一遍
    form.dnspod_id = ''
    form.dnspod_token = ''
    form.cloudflare_token = ''
    clears.dnspod = clears.cloudflare = false
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

  function stagingHint(): string {
    return 'Let’s Encrypt staging：签出来的证书**浏览器不认**，只用于调试。' +
      '确认流程跑通后再切到正式环境——正式环境每个域名每周只能签 5 张。'
  }

  function payload(): Record<string, unknown> {
    const body: Record<string, unknown> = {
      kind: form.kind,
      acme_email: form.acme_email,
      acme_directory: form.acme_directory,
    }
    if (form.dnspod_id) body.dnspod_id = form.dnspod_id
    if (form.dnspod_token) body.dnspod_token = form.dnspod_token
    if (form.cloudflare_token) body.cloudflare_token = form.cloudflare_token
    if (clears.dnspod) body.clear_dnspod = true
    if (clears.cloudflare) body.clear_cloudflare = true
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

  const clearDnspod = () => (clears.dnspod = true)
  const clearCloudflare = () => (clears.cloudflare = true)

  return {
    form, dnspodConfigured, cloudflareConfigured, staging, loading, saving, error,
    load, save, stagingHint, clearDnspod, clearCloudflare, __setIO,
  }
})
