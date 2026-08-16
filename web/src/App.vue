<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import { useSessionStore } from '@/stores/session'
import { useNodesStore } from '@/stores/nodes'
import { useDeployStore } from '@/stores/deploys'
import { useEventsStore } from '@/stores/events'
import { connectWS } from '@/api/ws'

const route = useRoute()
const session = useSessionStore()
const nodes = useNodesStore()
const deploys = useDeployStore()
const events = useEventsStore()
const theme = ref<'light' | 'dark'>('light')
let closeWS: (() => void) | null = null

const groups = [
  { label: '调度', items: [['/overview', '集群总览'], ['/nodes', '边缘节点'], ['/dns', 'DNS 调度']] },
  { label: '配置', items: [['/workbench', '配置工作台'], ['/routes', '反代路由'], ['/acl', '访问控制'], ['/certs', '证书']] },
  { label: '运维', items: [['/deploys', '下发记录'], ['/audit', '审计日志'], ['/alerts', '告警通知'], ['/settings', '系统设置']] },
]

function toggleTheme() {
  theme.value = theme.value === 'light' ? 'dark' : 'light'
  document.documentElement.dataset.theme = theme.value
}

onMounted(() => {
  // 一条通道，三类帧各自分发。每个 store 只认自己那一类，
  // 不认识的直接忽略——加新帧类型时不必改这里。
  closeWS = connectWS((frame) => {
    nodes.applyFrame(frame)
    deploys.applyFrame(frame)
    events.applyFrame(frame)
  })
})
onUnmounted(() => closeWS?.())
</script>

<template>
  <RouterView v-if="route.meta.public" />
  <div v-else class="shell">
    <aside class="rail">
      <div class="brand">
        <div class="logo">EC</div>
        <div>
          <div class="bt">Edge Controller</div>
          <div class="bs mono">master · {{ nodes.baseline || '尚未发布' }}</div>
        </div>
      </div>
      <nav>
        <template v-for="g in groups" :key="g.label">
          <div class="glabel">{{ g.label }}</div>
          <RouterLink v-for="[to, label] in g.items" :key="to" :to="to" class="nav">{{ label }}</RouterLink>
        </template>
      </nav>
    </aside>
    <main>
      <header class="top">
        <div class="ttl">{{ route.meta.title ?? route.name }}</div>
        <div class="right">
          <span class="mono who">{{ session.user || '未鉴权' }}</span>
          <button class="ghost" :aria-label="theme === 'light' ? '切换到深色' : '切换到浅色'" @click="toggleTheme">
            {{ theme === 'light' ? '☾' : '☀' }}
          </button>
        </div>
      </header>
      <div class="body"><RouterView /></div>
    </main>
  </div>
</template>

<style scoped>
.shell { display: grid; grid-template-columns: 210px 1fr; min-height: 100vh; }
.rail { background: var(--surface-card); border-right: 1px solid var(--border-subtle); padding: 14px 10px; }
.brand { display: flex; align-items: center; gap: 9px; padding: 4px 6px 16px; }
.logo { width: 30px; height: 30px; border-radius: 8px; display: grid; place-items: center; background: linear-gradient(135deg, var(--accent), var(--cyan-400)); color: #fff; font-weight: 700; font-size: 11.5px; }
.bt { font-size: 13px; font-weight: 700; color: var(--text-strong); }
.bs { font-size: 10px; color: var(--text-faint); }
.glabel { font-size: 10px; letter-spacing: .08em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; padding: 12px 8px 4px; }
.nav { display: block; padding: 7px 10px; border-radius: 8px; font-size: 13px; color: var(--text-body); text-decoration: none; }
.nav:hover { background: var(--surface-sunken); }
.nav.router-link-active { background: var(--accent-subtle); color: var(--accent-text); font-weight: 600; }
.top { display: flex; align-items: center; justify-content: space-between; padding: 14px 20px; border-bottom: 1px solid var(--border-subtle); }
.ttl { font-size: 15px; font-weight: 700; color: var(--text-strong); }
.right { display: flex; align-items: center; gap: 12px; }
.who { font-size: 11.5px; color: var(--text-muted); }
.ghost { background: none; border: 1px solid var(--border-subtle); border-radius: 8px; width: 30px; height: 30px; cursor: pointer; color: var(--text-body); }
.body { padding: 18px 20px; }
</style>
