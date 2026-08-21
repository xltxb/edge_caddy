<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { http } from '@/api/http'
import type { AlertTestWire, AlertsWire, NotifyLevel } from '@/api/types'
import { useUiStore } from '@/stores/ui'

/**
 * 告警通知。
 *
 * 通知级别是**渠道共用**的一个值（契约 §11），不是每个渠道各配一个 ——
 * 分开配会让人以为可以「Lark 只收严重的、Webhook 收全部」，而后端并不支持。
 */
const ui = useUiStore()

const saved = ref<AlertsWire | null>(null)
const form = ref<AlertsWire | null>(null)
const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const error = ref<string | null>(null)
const newWebhook = ref('')
const newLark = ref('')

const LEVELS: [NotifyLevel, string, string][] = [
  ['all', '全部', '每一条事件都推送，包括流水账'],
  ['warn', '异常及以上', '只推送需要看一眼的'],
  ['crit', '仅严重', '只推送必须马上处理的'],
]

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    saved.value = await http.get<AlertsWire>('/alerts')
    form.value = JSON.parse(JSON.stringify(saved.value)) as AlertsWire
    newWebhook.value = ''
    newLark.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载告警设置失败'
  } finally {
    loading.value = false
  }
}

onMounted(load)

const dirty = computed(
  () =>
    !!form.value &&
    !!saved.value &&
    (JSON.stringify(form.value) !== JSON.stringify(saved.value) ||
      newWebhook.value.length > 0 ||
      newLark.value.length > 0),
)

const levelHint = computed(
  () => LEVELS.find(([v]) => v === form.value?.notify_level)?.[2] ?? '',
)

async function save(): Promise<void> {
  if (!form.value) return
  saving.value = true
  try {
    const body: Record<string, unknown> = { ...form.value }
    if (newWebhook.value) body.webhook_url = newWebhook.value
    if (newLark.value) body.lark_webhook = newLark.value
    saved.value = await http.put<AlertsWire>('/alerts', body)
    form.value = JSON.parse(JSON.stringify(saved.value)) as AlertsWire
    newWebhook.value = ''
    newLark.value = ''
    ui.toast('ok', '告警设置已保存')
  } catch (e) {
    ui.toast('warn', '保存失败', e instanceof Error ? e.message : '')
  } finally {
    saving.value = false
  }
}

async function sendTest(): Promise<void> {
  testing.value = true
  try {
    const r = await http.post<AlertTestWire>('/alerts/test', { channel: 'lark' })
    ui.toast('ok', '测试卡片已发出', r.detail)
  } catch (e) {
    // 下游失败时 msg 带着服务商的原文错误 —— 那是排查 webhook 配错的唯一线索，
    // 所以这里原样呈出来，不要包装成「发送失败，请重试」。
    ui.toast('danger', '测试卡片发送失败', e instanceof Error ? e.message : '')
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="title">告警通知</div>
      <div class="sub">通知级别为两个渠道共用</div>
      <button class="mini" type="button" :disabled="!dirty" @click="load">放弃改动</button>
      <button class="primary" type="button" :disabled="!dirty || saving" @click="save">
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
        <div class="group-title">通知级别</div>
        <div class="row">
          <label>推送哪些事件</label>
          <div class="ctl">
            <div class="seg">
              <button
                v-for="[value, label] in LEVELS"
                :key="value"
                type="button"
                :class="{ on: form.notify_level === value }"
                @click="form.notify_level = value"
              >
                {{ label }}
              </button>
            </div>
            <p class="note">{{ levelHint }} · 两个渠道共用这一个级别。</p>
          </div>
        </div>
      </section>

      <section class="group">
        <div class="group-title">通用 Webhook</div>
        <div class="row">
          <label>地址</label>
          <div class="ctl">
            <span class="tag" :class="form.webhook.url_configured ? 'ok' : 'warn'">
              {{ form.webhook.url_configured ? '已配置' : '未配置' }}
            </span>
            <input
              v-model="newWebhook"
              class="text"
              type="password"
              autocomplete="off"
              placeholder="留空则不改动"
            />
            <p class="note">JSON POST，失败重试 3 次。地址不回显。</p>
          </div>
        </div>
      </section>

      <section class="group">
        <div class="group-title">Lark 群机器人</div>
        <div class="row">
          <label>Webhook</label>
          <div class="ctl">
            <span class="tag" :class="form.lark.webhook_configured ? 'ok' : 'warn'">
              {{ form.lark.webhook_configured ? '已配置' : '未配置' }}
            </span>
            <input
              v-model="newLark"
              class="text"
              type="password"
              autocomplete="off"
              placeholder="留空则不改动"
            />
            <button class="mini" type="button" :disabled="testing" @click="sendTest">
              {{ testing ? '发送中…' : '发送测试卡片' }}
            </button>
            <p class="note">测试发送会写入审计。</p>
          </div>
        </div>
        <div class="row">
          <label>严重告警 @所有人</label>
          <div class="ctl">
            <label class="switch">
              <input v-model="form.lark.at_all_on_crit" type="checkbox" />
              <span>
                {{ form.lark.at_all_on_crit ? '严重告警的卡片里附 @所有人' : '不 @任何人' }}
              </span>
            </label>
            <p v-if="form.lark.at_all_on_crit" class="note">
              只对 crit 级别生效。级别设成「全部」时不会因此变得更吵。
            </p>
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
