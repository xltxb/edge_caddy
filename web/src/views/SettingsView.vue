<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { http, errorText } from '@/api/http'
import type { DnsProviderPatch, SettingsWire } from '@/api/types'
import { useUiStore } from '@/stores/ui'
import { isDomain } from '@/utils/validators'

/**
 * 系统设置。
 *
 * 两条契约要求直接决定了这一页的形状：
 * 1. `master_endpoint` **必须是域名不是 IP**（后端会拒，code 1001）——本地先拦一次。
 * 2. **凭证只写入不回显**：响应里永远没有明文，只有 `configured: true/false`。
 *    所以这里不做「显示当前凭证」，只做「已配置 / 更换」。
 */
const ui = useUiStore()

const saved = ref<SettingsWire | null>(null)
const form = ref<SettingsWire | null>(null)
const loading = ref(false)
const saving = ref(false)
const error = ref<string | null>(null)
/** 更换凭证时才出现的明文输入框。空 = 不改动。 */

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    saved.value = await http.get<SettingsWire>('/settings')
    form.value = JSON.parse(JSON.stringify(saved.value)) as SettingsWire
  } catch (e) {
    error.value = errorText(e, '加载设置失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)

const dirty = computed(
  () =>
    !!form.value &&
    !!saved.value &&
    (JSON.stringify(form.value) !== JSON.stringify(saved.value) || dnsDirty.value),
)

/** 「节点最长 N 秒后被摘除」—— 前端算，不需要后端给（契约 §11）。 */
const dropAfter = computed(() =>
  form.value ? form.value.heartbeat_interval_s * form.value.offline_threshold_count : 0,
)

const endpointError = computed(() => {
  const v = form.value?.master_endpoint ?? ''
  if (!v) return '不能为空'
  const host = v.split(':')[0] ?? ''
  return isDomain(host) ? null : '必须是域名，不能是 IP —— 换 IP 时所有节点都要重装'
})

/**
 * DNS 服务商那一块的**可写字段**。
 *
 * 与 `form.dns_provider`（`GET` 回的那份）分开放，因为两者的字段集不一样：
 * 读回来的没有 `zone_id` / `email` / `account_id`（那些是凭证的一部分，
 * 契约 §11 说凭证不回显），所以它们只有「这次要不要改」这一种状态。
 */
const dnsEdit = ref<DnsProviderPatch>({})

/** 这一次要不要动 DNS 服务商 —— 什么都没填就不带这个对象，免得覆盖已配的。 */
const dnsDirty = computed(() => Object.values(dnsEdit.value).some((v) => v !== undefined && v !== ''))

/** Cloudflare 的两种凭证方式要的字段不同（契约 §11）。 */
const cfMode = computed(() => dnsEdit.value.credential_mode ?? form.value?.dns_provider.credential_mode ?? '')
const kindNow = computed(() => dnsEdit.value.kind ?? form.value?.dns_provider.kind ?? '')

async function save(): Promise<void> {
  if (!form.value || endpointError.value) return
  saving.value = true
  try {
    /*
     * **`dns_provider` 的字段嵌在那个对象里，不在顶层。**
     *
     * 这里原先发的是顶层的 `dns_credential` —— 后端不认识那个 key，
     * 静默忽略并回 `code: 0`，于是界面弹「设置已保存」而什么都没存进去。
     * 一个成功的假象比一个报错难查得多：报错会让人再试，假象让人走开。
     * （真主控上核实过两种形状：顶层的 configured 仍是 false，嵌套的才 true。）
     *
     * 没动过就整个不带 `dns_provider` —— 带一个空对象会把已配的字段清掉。
     */
    const { dns_provider: _dns, ...rest } = form.value
    const body: Record<string, unknown> = { ...rest }
    if (dnsDirty.value) {
      const p: DnsProviderPatch = {}
      for (const [k, v] of Object.entries(dnsEdit.value)) {
        if (v !== undefined && v !== '') (p as Record<string, unknown>)[k] = v
      }
      body.dns_provider = p
    }
    saved.value = await http.put<SettingsWire>('/settings', body)
    form.value = JSON.parse(JSON.stringify(saved.value)) as SettingsWire
    dnsEdit.value = {}
    ui.toast('ok', '设置已保存')
  } catch (e) {
    ui.toast('warn', '保存失败', errorText(e, ''))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">系统设置</div>
      <div class="sub">凭证只写入不回显</div>
      <button class="mini" type="button" :disabled="!dirty" @click="load">放弃改动</button>
      <button
        class="primary"
        type="button"
        :disabled="!dirty || saving || !!endpointError"
        @click="save"
      >
        {{ saving ? '保存中…' : '保存' }}
      </button>
    </header>

    <div v-if="loading && !form" class="hint">正在加载…</div>
    <div v-else-if="error" class="hint error">
      {{ error }}
      <button class="mini" type="button" @click="load">重试</button>
    </div>

    <div v-else-if="form" class="groups">
      <section class="group">
        <div class="group-title">主控接入</div>
        <div class="row">
          <label>Agent 连接地址</label>
          <div class="ctl">
            <input
              v-model="form.master_endpoint"
              class="text"
              :class="{ bad: !!endpointError }"
              type="text"
            />
            <p v-if="endpointError" class="err">{{ endpointError }}</p>
            <p v-else class="note">
              Agent 主动外连这个地址。用域名而不是 IP，换机器时不必重装每一台节点。
            </p>
          </div>
        </div>
      </section>

      <section class="group">
        <div class="group-title">探活与自愈</div>
        <div class="row">
          <label>心跳间隔（秒）</label>
          <div class="ctl">
            <input v-model.number="form.heartbeat_interval_s" class="text narrow" type="text" />
          </div>
        </div>
        <div class="row">
          <label>连续超时次数</label>
          <div class="ctl">
            <input v-model.number="form.offline_threshold_count" class="text narrow" type="text" />
            <!-- 联动提示：两个数字单看都没意义，乘起来才是人关心的那件事 -->
            <p class="note strong">
              节点最长 <b>{{ dropAfter }}</b> 秒后被判定离线。
            </p>
          </div>
        </div>
        <div class="row">
          <label>自动摘除解析</label>
          <div class="ctl">
            <label class="switch">
              <input v-model="form.auto_drop_dns" type="checkbox" />
              <span>{{ form.auto_drop_dns ? '判定离线后自动退出 DNS 解析' : '判定离线后仍保留解析权重' }}</span>
            </label>
            <p v-if="!form.auto_drop_dns" class="note warn">
              关闭后，离线节点仍会分到流量，直到有人手动暂停它。
            </p>
          </div>
        </div>
      </section>

      <!--
        这一块原先只能改「凭证」，服务商 / 域名 / 凭证方式全是只读文本 ——
        而 kind 为空时那一行是**一片空白**。灰度环境上人的反馈是「找不到配置
        DNS 服务商的地方」，他分不出是没做还是没找到，而这两件事他的下一步
        完全不同（一个是等我们做，一个是再找找）。

        又一次「没有」表现成「我没找到」，这次是人类撞上的。
      -->
      <section class="group">
        <div class="group-title">DNS 服务商</div>

        <p v-if="!form.dns_provider.kind" class="group-lead warn">
          还没配。证书签发（DNS-01）、解析权重下发、节点下线时摘解析，
          三件事都要等这里填上。
        </p>

        <div class="row">
          <label for="dns-kind">服务商</label>
          <div class="ctl">
            <select id="dns-kind" v-model="dnsEdit.kind" class="text sel">
              <option value="">{{ form.dns_provider.kind || '（未选择）' }}</option>
              <option value="cloudflare">Cloudflare</option>
              <option value="dnspod">DNSPod</option>
            </select>
            <p class="note">
              两家能表达的东西不一样：Cloudflare 的 DNS 记录没有线路概念，
              电信 / 联通 / 移动会被合并成「中国」。选定之后 DNS 调度页会按它的
              能力合并输入框。
            </p>
          </div>
        </div>

        <div class="row">
          <label for="dns-domain">解析域名</label>
          <div class="ctl">
            <input
              id="dns-domain"
              v-model="dnsEdit.domain"
              class="text"
              :placeholder="form.dns_provider.domain || 'example.com'"
            />
            <p class="note">记录写在这个域名下。留空 = 不改动当前值。</p>
          </div>
        </div>

        <div class="row">
          <label for="dns-sub">子域前缀</label>
          <div class="ctl">
            <input
              id="dns-sub"
              v-model="dnsEdit.sub"
              class="text"
              :placeholder="form.dns_provider.sub || '（可空）'"
            />
            <p class="note">留空则记录直接写在解析域名上。</p>
          </div>
        </div>

        <!-- Cloudflare 两种凭证方式要的字段不同（契约 §11） -->
        <div v-if="kindNow === 'cloudflare'" class="row">
          <label for="dns-mode">凭证方式</label>
          <div class="ctl">
            <select id="dns-mode" v-model="dnsEdit.credential_mode" class="text sel">
              <option value="">{{ form.dns_provider.credential_mode || '（未选择）' }}</option>
              <option value="api_token">API Token（推荐，权限可收窄到单个 zone）</option>
              <option value="global_key">Global API Key</option>
            </select>
          </div>
        </div>

        <div v-if="kindNow === 'cloudflare'" class="row">
          <label for="dns-zone">Zone ID</label>
          <div class="ctl">
            <input id="dns-zone" v-model="dnsEdit.zone_id" class="text mono" placeholder="留空 = 不改动" />
          </div>
        </div>

        <template v-if="kindNow === 'cloudflare' && cfMode === 'global_key'">
          <div class="row">
            <label for="dns-email">账号邮箱</label>
            <div class="ctl">
              <input id="dns-email" v-model="dnsEdit.email" class="text" placeholder="Global Key 模式必填" />
            </div>
          </div>
          <div class="row">
            <label for="dns-account">Account ID</label>
            <div class="ctl">
              <input id="dns-account" v-model="dnsEdit.account_id" class="text mono" placeholder="可空" />
            </div>
          </div>
        </template>

        <div class="row">
          <label for="dns-cred">凭证</label>
          <div class="ctl">
            <span class="tag" :class="form.dns_provider.configured ? 'ok' : 'warn'">
              {{ form.dns_provider.configured ? '已配置' : '未配置' }}
            </span>
            <input
              id="dns-cred"
              v-model="dnsEdit.credential"
              class="text"
              type="password"
              autocomplete="off"
              :placeholder="form.dns_provider.configured ? '留空 = 不改动现有凭证' : '粘贴凭证'"
            />
            <!--
              凭证只写入不回显（契约 §11）。这不是懒得做「查看」——
              回显一次就等于给了它一条泄露路径，而它能改写整个 zone。
            -->
            <p class="note">
              {{
                kindNow === 'dnspod'
                  ? 'DNSPod 的形式是「ID,Token」，中间一个逗号。'
                  : 'Cloudflare 填 API Token 或 Global API Key。'
              }}
              只写入不回显，换新的会让旧凭证立即失效。
            </p>
          </div>
        </div>

        <div class="row">
          <label>ops-bot Token</label>
          <div class="ctl">
            <span class="tag" :class="form.ops_bot_token_configured ? 'ok' : 'warn'">
              {{ form.ops_bot_token_configured ? '已配置' : '未配置' }}
            </span>
            <p class="note">自动化账号用它走 Bearer 鉴权，与人的会话 Cookie 分开。</p>
          </div>
        </div>
      </section>
    </div>
  </section>
</template>

<style scoped>
@import './catalog.css';
@import './settings.css';
</style>
