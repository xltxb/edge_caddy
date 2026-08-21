<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppSidebar from '@/components/layout/AppSidebar.vue'
import AppTopbar from '@/components/layout/AppTopbar.vue'
import CommandPalette from '@/components/palette/CommandPalette.vue'
import ErrorBoundary from '@/components/layout/ErrorBoundary.vue'
import ToastHost from '@/components/layout/ToastHost.vue'
import { startRealtime, stopRealtime } from '@/realtime'
import { NAV } from '@/router/nav'
import { useConfigStore } from '@/stores/config'
import { useNodesStore } from '@/stores/nodes'
import { useOverviewStore } from '@/stores/overview'
import { useSessionStore } from '@/stores/session'
import { useUiStore } from '@/stores/ui'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const nodes = useNodesStore()
const overview = useOverviewStore()
const config = useConfigStore()
const ui = useUiStore()

const bare = computed(() => route.meta.layout === 'bare')

const title = computed(
  () => NAV.find((n) => route.path.startsWith(n.path))?.label ?? 'Edge Controller',
)

/*
 * 侧栏角标。
 *
 * 只显示**外壳本来就加载**的数据。下发记录与审计日志是 cursor 分页的流，
 * 拿第一页的长度当计数会显示成页大小而不是真实总数 —— 那是在撒一个
 * 没人会去核对的谎，不如不显示。
 */
const counts = computed<Record<string, number | string>>(() => ({
  overview: nodes.items.length || '',
  nodes: nodes.items.length || '',
  dns: nodes.items.filter((n) => n.dnsEnabled).length || '',
  // 与工作台底栏同源。两个地方各算一遍迟早会对不上，而对不上的那个数字
  // 恰好长在「怕推错」这条主线上。
  workbench: config.totalChanges || '',
  routes: config.routes.length || '',
  acl: config.rules.length || '',
}))

async function loadShell(): Promise<void> {
  await Promise.all([
    overview.fetch().catch(() => {}),
    nodes.fetchAll().catch(() => {}),
    config.fetchAll().catch(() => {}),
  ])
  // 漂移只由「上报版本号 vs 基线」决定（ADR-0002），基线到位后统一重算一次
  if (overview.baseline) nodes.recomputeDrift(overview.baseline)
}

// 登录成功后才装配外壳与实时通道；登出时拆掉，别让 socket 在登录页上空转
watch(
  () => session.operator,
  (op) => {
    if (op) {
      void loadShell()
      startRealtime()
    } else {
      stopRealtime()
    }
  },
  { immediate: true },
)

/**
 * 页面卸载前把还没落地的草稿送出去。
 *
 * 没有这一步，「改一个字段 → 立刻刷新」会静默丢掉那次改动 ——
 * e2e 就是在 reload 那一步抓到它的。
 */
function onBeforeUnload(): void {
  config.flush({ keepalive: true })
}

function onHotkey(e: KeyboardEvent): void {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    ui.paletteOpen = true
  }
  if (e.key === 'Escape') ui.paletteOpen = false
}

onMounted(() => {
  window.addEventListener('keydown', onHotkey)
  window.addEventListener('beforeunload', onBeforeUnload)
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onHotkey)
  window.removeEventListener('beforeunload', onBeforeUnload)
  stopRealtime()
})

// SPA 内部切页时同样要落地：路由守卫比 beforeunload 可靠，能 await
watch(
  () => route.fullPath,
  () => config.flush(),
)
</script>

<template>
  <RouterView v-if="bare" />

  <div v-else class="shell">
    <AppSidebar :counts="counts" />
    <main class="main">
      <AppTopbar
        :title="title"
        :baseline="overview.baseline || '—'"
        :pending-count="config.totalChanges"
        :node-count="nodes.items.length"
        @palette="ui.paletteOpen = true"
        @deploy="router.push({ name: 'workbench' })"
      />
      <div class="page">
        <!-- 渲染期异常不该表现成白屏 —— 见 ErrorBoundary 里的说明 -->
        <ErrorBoundary>
          <RouterView />
        </ErrorBoundary>
      </div>
    </main>
  </div>

  <CommandPalette />
  <ToastHost />
</template>

<style scoped>
.shell {
  display: flex;
  min-height: 100vh;
  align-items: stretch;
}
.main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.page {
  padding: var(--space-5) var(--space-6) var(--space-8);
  display: flex;
  flex-direction: column;
  gap: 18px;
}
</style>
