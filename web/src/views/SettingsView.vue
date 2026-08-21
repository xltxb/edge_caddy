<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { http } from '@/api/http'
import type { SettingsWire } from '@/api/types'
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
const newCredential = ref('')
const changingCred = ref(false)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    saved.value = await http.get<SettingsWire>('/settings')
    form.value = JSON.parse(JSON.stringify(saved.value)) as SettingsWire
    newCredential.value = ''
    changingCred.value = false
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载设置失败'
  } finally {
    loading.value = false
  }
}

onMounted(load)

const dirty = computed(
  () =>
    !!form.value &&
    !!saved.value &&
    (JSON.stringify(form.value) !== JSON.stringify(saved.value) || newCredential.value.length > 0),
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

async function save(): Promise<void> {
  if (!form.value || endpointError.value) return
  saving.value = true
  try {
    // 不带凭证字段 = 保持不变；带了就是替换（契约 §11）
    const body: Record<string, unknown> = { ...form.value }
    if (newCredential.value) body.dns_credential = newCredential.value
    saved.value = await http.put<SettingsWire>('/settings', body)
    form.value = JSON.parse(JSON.stringify(saved.value)) as SettingsWire
    newCredential.value = ''
    changingCred.value = false
    ui.toast('ok', '设置已保存')
  } catch (e) {
    ui.toast('warn', '保存失败', e instanceof Error ? e.message : '')
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

      <section class="group">
        <div class="group-title">DNS 服务商</div>
        <div class="row">
          <label>服务商</label>
          <div class="ctl">
            <span class="mono">{{ form.dns_provider.kind }}</span>
            <span class="mono muted">
              · 凭证方式 {{ form.dns_provider.credential_mode === 'api_token' ? 'API Token' : 'Global Key' }}
            </span>
          </div>
        </div>
        <div class="row">
          <label>凭证</label>
          <div class="ctl">
            <template v-if="!changingCred">
              <span class="tag" :class="form.dns_provider.configured ? 'ok' : 'warn'">
                {{ form.dns_provider.configured ? '已配置' : '未配置' }}
              </span>
              <button class="mini" type="button" @click="changingCred = true">更换凭证</button>
              <!--
                凭证只写入不回显（契约 §11）。这不是懒得做「查看」——
                回显一次就等于给了它一条泄露路径，而它能改写整个 zone。
              -->
              <p class="note">当前凭证不会回显。更换后旧凭证立即失效。</p>
            </template>
            <template v-else>
              <input
                v-model="newCredential"
                class="text"
                type="password"
                autocomplete="off"
                placeholder="粘贴新的凭证"
              />
              <button class="mini" type="button" @click="(changingCred = false), (newCredential = '')">
                取消
              </button>
              <p class="note">留空并保存不会改动凭证。</p>
            </template>
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
