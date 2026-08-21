<script setup lang="ts">
import LinkIndicator from './LinkIndicator.vue'
import { useUiStore } from '@/stores/ui'

defineProps<{
  title: string
  baseline: string
  pendingCount: number
  nodeCount: number
}>()
defineEmits<{ (e: 'palette'): void; (e: 'deploy'): void }>()

const ui = useUiStore()
</script>

<template>
  <header class="topbar">
    <div class="title">{{ title }}</div>

    <div class="stat">
      <span class="k">当前基线</span>
      <b class="v">{{ baseline }}</b>
    </div>
    <div class="stat">
      <span class="k">待下发</span>
      <b class="v" :class="{ hot: pendingCount > 0 }">
        {{ pendingCount > 0 ? `${pendingCount} 处变更` : '无' }}
      </b>
    </div>

    <LinkIndicator />

    <button
      class="ghost icon"
      type="button"
      :title="ui.theme === 'dark' ? '切换到浅色' : '切换到深色'"
      :aria-label="ui.theme === 'dark' ? '切换到浅色' : '切换到深色'"
      @click="ui.toggleTheme()"
    >
      <svg
        width="15"
        height="15"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <path v-if="ui.theme === 'dark'" d="M12 3v2M12 19v2M5 12H3M21 12h-2M6.3 6.3 4.9 4.9M19.1 19.1l-1.4-1.4M6.3 17.7l-1.4 1.4M19.1 4.9l-1.4 1.4" />
        <circle v-if="ui.theme === 'dark'" cx="12" cy="12" r="4" />
        <path v-else d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8" />
      </svg>
    </button>

    <button class="ghost" type="button" title="命令面板 ⌘K" @click="$emit('palette')">
      <svg
        width="14"
        height="14"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <circle cx="11" cy="11" r="7" />
        <path d="m20 20-3.5-3.5" />
      </svg>
      <span>命令</span>
      <span class="kbd">⌘K</span>
    </button>

    <button class="primary" type="button" :disabled="pendingCount === 0" @click="$emit('deploy')">
      下发到 {{ nodeCount }} 个节点 →
    </button>
  </header>
</template>

<style scoped>
.topbar {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  padding: var(--space-3) var(--space-6);
  border-bottom: 1px solid var(--border-subtle);
  background: var(--surface-card);
  position: sticky;
  top: 0;
  z-index: var(--z-sticky);
}
.title {
  font-family: var(--font-display);
  font-size: var(--fs-lg);
  font-weight: var(--weight-bold);
  letter-spacing: var(--tracking-tight);
  color: var(--text-strong);
  margin-right: auto;
}
.stat {
  /* 标签在上、值在下 —— 与设计稿一致，也让值本身更容易被扫到 */
  display: flex;
  flex-direction: column;
  gap: 1px;
  white-space: nowrap;
}
.stat .k {
  font-size: var(--fs-micro);
  color: var(--text-muted);
}
.stat .v {
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
  font-weight: var(--weight-semibold);
  color: var(--text-strong);
}
.stat .v.hot {
  color: var(--accent-text);
}
.ghost {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1-5);
  padding: 5px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-muted);
  font-size: var(--fs-xs);
  cursor: pointer;
  transition: var(--transition-colors);
}
.ghost:hover {
  background: var(--surface-sunken);
  color: var(--text-strong);
}
.ghost.icon {
  padding: 5px 7px;
}
.kbd {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-faint);
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
  transition: var(--transition-colors);
}
.primary:hover:not(:disabled) {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
  box-shadow: var(--glow-sm);
}
.primary:active:not(:disabled) {
  transform: translateY(1px) scale(0.99);
}
</style>
