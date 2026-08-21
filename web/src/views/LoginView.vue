<script setup lang="ts">
import { ref } from 'vue'
import { errorText } from '@/api/http'
import { useRoute, useRouter } from 'vue-router'
import { useSessionStore } from '@/stores/session'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()

const username = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit(): Promise<void> {
  error.value = ''
  busy.value = true
  try {
    await session.login(username.value, password.value)
    const to = (route.query.redirect as string) || '/overview'
    await router.replace(to)
  } catch (e) {
    // 失败原因由后端给（用户可读中文），登录失败会写审计并在审计页单独提示
    error.value = errorText(e, '登录失败')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <!--
    设计稿没有画登录页（11 个 screen label 里没有它）。这里按设计稿的视觉语言
    自建：侧边栏那块 azure→cyan 渐变徽标 + 居中卡片 + Vela 语义令牌，
    不发明新的视觉。
  -->
  <div class="wrap">
    <form class="card" @submit.prevent="submit">
      <div class="brand">
        <div class="mark">EC</div>
        <div>
          <div class="name">Edge Controller</div>
          <div class="env">prod · master-hk</div>
        </div>
      </div>

      <label class="field">
        <span class="label">用户名</span>
        <input
          v-model="username"
          type="text"
          autocomplete="username"
          autofocus
          :disabled="busy"
          required
        />
      </label>

      <label class="field">
        <span class="label">密码</span>
        <input
          v-model="password"
          type="password"
          autocomplete="current-password"
          :disabled="busy"
          required
        />
      </label>

      <p v-if="error" class="error" role="alert">{{ error }}</p>

      <button class="submit" type="submit" :disabled="busy">
        {{ busy ? '登录中…' : '登录' }}
      </button>

      <p class="foot">控制台只在内网可达，登录成功与失败都会写入审计。</p>
    </form>
  </div>
</template>

<style scoped>
.wrap {
  min-height: 100vh;
  display: grid;
  place-items: center;
  background: var(--surface-page);
  padding: var(--space-6);
}
.card {
  width: 100%;
  max-width: 360px;
  background: var(--surface-card);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
  padding: var(--space-6);
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: var(--space-1);
}
.mark {
  width: 32px;
  height: 32px;
  flex: none;
  border-radius: var(--radius-sm);
  background: linear-gradient(135deg, var(--azure-500), var(--cyan-400));
  display: grid;
  place-items: center;
  color: var(--text-on-accent);
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  font-weight: var(--weight-bold);
  box-shadow: var(--shadow-xs);
}
.name {
  font-family: var(--font-display);
  font-size: var(--fs-base);
  font-weight: var(--weight-bold);
  letter-spacing: var(--tracking-tight);
  color: var(--text-strong);
}
.env {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-muted);
}
.field {
  display: flex;
  flex-direction: column;
  gap: var(--space-1-5);
}
.label {
  font-size: var(--fs-xs);
  color: var(--text-muted);
}
input {
  padding: 8px 11px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-sm);
  transition: var(--transition-colors);
}
input:focus {
  border-color: var(--accent);
}
.error {
  margin: 0;
  padding: 8px 11px;
  border-radius: var(--radius-sm);
  background: var(--danger-subtle);
  color: var(--danger-text);
  font-size: var(--fs-xs);
}
.submit {
  padding: 9px 14px;
  border: 1px solid var(--accent);
  border-radius: var(--radius-sm);
  background: var(--accent);
  color: var(--text-on-accent);
  font-size: var(--fs-sm);
  font-weight: var(--weight-semibold);
  cursor: pointer;
  transition: var(--transition-colors);
}
.submit:hover:not(:disabled) {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
  box-shadow: var(--glow-sm);
}
.foot {
  margin: 0;
  font-size: var(--fs-2xs);
  color: var(--text-faint);
  text-align: center;
}
</style>
