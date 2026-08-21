<script setup lang="ts">
import { useUiStore } from '@/stores/ui'

const ui = useUiStore()

const COLOR: Record<string, string> = {
  ok: 'var(--success)',
  info: 'var(--accent)',
  warn: 'var(--warning)',
  danger: 'var(--danger)',
}
</script>

<template>
  <div class="host" role="region" aria-live="polite" aria-label="操作结果">
    <div v-for="t in ui.toasts" :key="t.id" class="toast">
      <span class="bar" :style="{ background: COLOR[t.kind] }" />
      <div class="body">
        <div class="title">{{ t.title }}</div>
        <div v-if="t.detail" class="detail">{{ t.detail }}</div>
      </div>
      <button class="close" type="button" aria-label="关闭" @click="ui.dismiss(t.id)">×</button>
    </div>
  </div>
</template>

<style scoped>
.host {
  position: fixed;
  right: var(--space-5);
  bottom: var(--space-5);
  z-index: var(--z-toast);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  pointer-events: none;
}
.toast {
  pointer-events: auto;
  display: flex;
  align-items: stretch;
  gap: var(--space-3);
  min-width: 280px;
  max-width: 380px;
  background: var(--surface-raised);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  overflow: hidden;
}
.bar {
  width: 3px;
  flex: none;
}
.body {
  padding: 10px 0;
  min-width: 0;
  flex: 1;
}
.title {
  font-size: var(--fs-xs);
  font-weight: var(--weight-semibold);
  color: var(--text-strong);
}
.detail {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-muted);
  margin-top: 2px;
  word-break: break-all;
}
.close {
  border: 0;
  background: transparent;
  color: var(--text-faint);
  cursor: pointer;
  padding: 0 10px;
  font-size: var(--fs-lg);
  line-height: 1;
}
.close:hover {
  color: var(--text-strong);
}
</style>
