<script setup lang="ts">
defineProps<{ tone: 'ok' | 'warn' | 'danger' | 'muted'; text: string }>()
</script>

<template>
  <!-- 「色 + 文字」双编码（PRD §7 可达性）：不能只靠颜色区分状态。 -->
  <span class="pill" :class="tone"><i class="dot" />{{ text }}</span>
</template>

<style scoped>
.pill {
  display: inline-flex; align-items: center; gap: 5px; padding: 2px 8px;
  border-radius: 999px; font-size: 11.5px; font-weight: 600; white-space: nowrap;
}
.dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; flex: none; }
.ok { background: var(--success-subtle); color: var(--success-text); }
.warn { background: var(--warning-subtle); color: var(--warning-text); }
.danger { background: var(--danger-subtle); color: var(--danger-text); }
.muted { background: var(--surface-sunken); color: var(--text-muted); }
</style>
