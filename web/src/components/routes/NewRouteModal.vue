<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { http, ApiError } from '@/api/http'
import { CODE, type BlockMode } from '@/api/types'
import { useConfigStore } from '@/stores/config'
import { useUiStore } from '@/stores/ui'
import { isBodyMax, isDomain, isHostPort } from '@/utils/validators'

/**
 * 新建路由向导。
 *
 * 本地先拦一道（域名格式、`host:port`、重名），但**重名以后端为准** ——
 * 别人可能刚刚建了同名的，前端的列表未必是最新的。后端用 `code 1004` 拒绝，
 * 这里把它落到域名输入框上，而不是丢进一个泛泛的 toast。
 *
 * 新建的路由 version 为 0（尚未下发到任何节点），创建后直接跳工作台 ——
 * 建完就得配，把人留在目录页只会让他再点一次。
 */
const emit = defineEmits<{ (e: 'close'): void }>()
const router = useRouter()
const config = useConfigStore()
const ui = useUiStore()

const form = ref({
  domain: '',
  upstream: '',
  block_mode: 'abort' as BlockMode,
  body_max: '5MB',
})
const busy = ref(false)
const serverError = ref('')

const domainError = computed(() => {
  const v = form.value.domain.trim()
  if (!v) return ''
  if (!isDomain(v)) return '域名格式不正确'
  if (config.routes.some((r) => r.domain === v)) return '这个域名已经有一条路由了'
  return ''
})
const upstreamError = computed(() => {
  const v = form.value.upstream.trim()
  if (!v) return ''
  return isHostPort(v) ? '' : '回源地址必须形如 host:port'
})
const bodyMaxError = computed(() =>
  isBodyMax(form.value.body_max) ? '' : '格式应形如 5MB / 512KB',
)

const canSubmit = computed(
  () =>
    form.value.domain.trim() !== '' &&
    form.value.upstream.trim() !== '' &&
    !domainError.value &&
    !upstreamError.value &&
    !bodyMaxError.value,
)

async function submit(): Promise<void> {
  if (!canSubmit.value) return
  busy.value = true
  serverError.value = ''
  try {
    const domain = form.value.domain.trim()
    await http.post('/routes', {
      domain,
      upstream: form.value.upstream.trim(),
      block_mode: form.value.block_mode,
      mtls: false,
      compress: true,
      body_max: form.value.body_max,
      whitelist: [],
    })
    await config.fetchAll().catch(() => {})
    ui.toast('ok', `已创建 ${domain}`, '尚未下发到任何节点，配好后在工作台下发')
    emit('close')
    await router.push({ name: 'workbench', params: { key: `route:${domain}` } })
  } catch (e) {
    // 重名以后端为准：别人可能刚建了同名的，前端列表未必最新
    serverError.value =
      e instanceof ApiError && e.code === CODE.CONFLICT
        ? '这个域名已经有一条路由了（主控侧检出）'
        : e instanceof Error
          ? e.message
          : '创建失败'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="mask" @click.self="emit('close')">
    <form class="modal" role="dialog" aria-modal="true" aria-label="新建路由" @submit.prevent="submit">
      <div class="title">新建反代路由</div>
      <p class="lead">
        只需域名与回源地址。白名单、mTLS、压缩等在工作台里配，配好再一起下发。
      </p>

      <label class="field">
        <span>域名</span>
        <input v-model="form.domain" :class="{ bad: !!domainError }" placeholder="api.example.com" />
        <em v-if="domainError" class="err">{{ domainError }}</em>
      </label>

      <label class="field">
        <span>回源地址</span>
        <input v-model="form.upstream" :class="{ bad: !!upstreamError }" placeholder="10.8.0.2:8080" />
        <em v-if="upstreamError" class="err">{{ upstreamError }}</em>
        <em v-else class="hint">建议走 WireGuard 内网地址，源站防火墙只放行边缘节点 IP。</em>
      </label>

      <label class="field">
        <span>非白名单流量处置</span>
        <span class="seg">
          <button
            v-for="m in (['abort', '403', '404'] as BlockMode[])"
            :key="m"
            type="button"
            :class="{ on: form.block_mode === m }"
            @click="form.block_mode = m"
          >
            {{ m }}
          </button>
        </span>
        <em class="hint">
          {{
            form.block_mode === 'abort'
              ? 'abort 直接切断 TCP，扫描器无法嗅探到应用存在。'
              : `返回 ${form.block_mode}，会暴露该域名后有服务在运行。`
          }}
        </em>
      </label>

      <label class="field">
        <span>请求体上限</span>
        <input v-model="form.body_max" class="narrow" :class="{ bad: !!bodyMaxError }" />
        <em v-if="bodyMaxError" class="err">{{ bodyMaxError }}</em>
      </label>

      <p v-if="serverError" class="server-err">{{ serverError }}</p>

      <div class="actions">
        <button class="ghost" type="button" @click="emit('close')">取消</button>
        <button class="primary" type="submit" :disabled="!canSubmit || busy">
          {{ busy ? '创建中…' : '创建并去配置' }}
        </button>
      </div>
    </form>
  </div>
</template>

<style scoped>
.mask {
  position: fixed;
  inset: 0;
  background: var(--surface-overlay);
  z-index: var(--z-modal);
  display: grid;
  place-items: center;
  padding: var(--space-6);
}
.modal {
  width: min(460px, 100%);
  background: var(--surface-raised);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xl);
  padding: var(--space-5);
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.title {
  font-size: var(--fs-base);
  font-weight: var(--weight-bold);
  color: var(--text-strong);
}
.lead {
  margin: 0;
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.7;
}
.field {
  display: flex;
  flex-direction: column;
  gap: var(--space-1-5);
}
.field > span:first-child {
  font-size: var(--fs-xs);
  color: var(--text-body);
}
input {
  padding: 7px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
}
input.narrow {
  width: 130px;
}
input:focus {
  border-color: var(--accent);
  outline: none;
}
input.bad {
  border-color: var(--danger);
  background: var(--danger-subtle);
}
.seg {
  display: inline-flex;
  gap: 4px;
  padding: 2px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  align-self: flex-start;
}
.seg button {
  padding: 4px 12px;
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  cursor: pointer;
}
.seg button.on {
  background: var(--accent);
  color: var(--text-on-accent);
  font-weight: var(--weight-semibold);
}
.hint,
.err {
  font-style: normal;
  font-size: var(--fs-2xs);
  line-height: 1.6;
}
.hint {
  color: var(--text-muted);
}
.err {
  color: var(--danger-text);
}
.server-err {
  margin: 0;
  padding: 7px 10px;
  border-radius: var(--radius-sm);
  background: var(--danger-subtle);
  color: var(--danger-text);
  font-size: var(--fs-2xs);
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
  margin-top: var(--space-1);
}
.ghost {
  padding: 6px 14px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-xs);
  cursor: pointer;
}
.primary {
  padding: 6px 14px;
  border: 1px solid var(--accent);
  border-radius: var(--radius-sm);
  background: var(--accent);
  color: var(--text-on-accent);
  font-size: var(--fs-xs);
  font-weight: var(--weight-semibold);
  cursor: pointer;
}
</style>
