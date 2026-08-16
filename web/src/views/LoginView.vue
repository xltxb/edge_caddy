<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const router = useRouter()
const route = useRoute()

const user = ref('abiu')
const password = ref('')
const err = ref('')
const busy = ref(false)

async function submit() {
  if (!password.value || busy.value) return
  busy.value = true
  err.value = ''
  try {
    await session.login(user.value, password.value)
    // 回到登录前想去的地方，而不是一律丢回首页
    await router.replace(String(route.query.redirect ?? '/overview'))
  } catch (e) {
    err.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="wrap">
    <form class="card" @submit.prevent="submit">
      <div class="head">
        <div class="logo">EC</div>
        <div>
          <div class="title">Edge Controller</div>
          <div class="sub">控制台登录</div>
        </div>
      </div>

      <label class="f">
        <span>用户名</span>
        <input v-model.trim="user" class="inp mono" autocomplete="username" />
      </label>
      <label class="f">
        <span>口令</span>
        <input v-model="password" class="inp" type="password" autocomplete="current-password" />
      </label>

      <p v-if="err" class="err" role="alert">{{ err }}</p>

      <button class="btn" type="submit" :disabled="!password || busy">
        {{ busy ? '登录中…' : '登录' }}
      </button>
      <p class="hint">
        首次部署用 <span class="mono">EDGE_ADMIN_PASSWORD</span> 初始化口令；未设置时接口无鉴权。
      </p>
    </form>
  </div>
</template>

<style scoped>
.wrap { min-height: 100vh; display: grid; place-items: center; padding: 24px; }
.card {
  width: 340px; padding: 26px 28px 22px; border-radius: 16px;
  background: var(--surface-card); border: 1px solid var(--border-subtle);
  box-shadow: 0 12px 32px rgba(12, 14, 23, .1); display: flex; flex-direction: column; gap: 14px;
}
.head { display: flex; align-items: center; gap: 11px; margin-bottom: 4px; }
.logo {
  width: 34px; height: 34px; border-radius: 9px; display: grid; place-items: center;
  background: linear-gradient(135deg, var(--accent), var(--cyan-400));
  color: #fff; font-weight: 700; font-size: 12.5px; letter-spacing: .04em;
}
.title { font-size: 15.5px; font-weight: 700; color: var(--text-strong); }
.sub { font-size: 12px; color: var(--text-muted); }
.f { display: flex; flex-direction: column; gap: 5px; }
.f > span { font-size: 12px; font-weight: 600; color: var(--text-strong); }
.inp {
  padding: 9px 11px; border-radius: 9px; font-size: 13.5px;
  border: 1px solid var(--border-default); background: var(--surface-card); color: var(--text-strong);
}
.btn {
  margin-top: 2px; padding: 9px 14px; border: 0; border-radius: 9px; cursor: pointer;
  background: var(--accent); color: var(--text-on-accent); font-size: 13.5px; font-weight: 600;
}
.btn:hover:not(:disabled) { background: var(--accent-hover); }
.err {
  margin: 0; padding: 8px 11px; border-radius: 8px; font-size: 12.5px;
  background: var(--danger-subtle); color: var(--danger-text);
}
.hint { margin: 2px 0 0; font-size: 11px; line-height: 1.65; color: var(--text-faint); }
</style>
