<script setup lang="ts">
defineProps<{
  label: string
  value: string
  unit?: string
  foot: string
  footColor?: string
  active?: boolean
  /** 标签旁的 ⓘ 说明。有些 KPI 的名字会让人以为它能回答它其实回答不了的问题。 */
  caveat?: string
}>()
defineEmits<{ (e: 'select'): void }>()
</script>

<template>
  <button class="kpi" :class="{ active }" type="button" @click="$emit('select')">
    <span class="label">
      {{ label }}
      <span v-if="caveat" class="info" :title="caveat" role="note" :aria-label="caveat">ⓘ</span>
    </span>
    <span class="value">
      {{ value }}<small v-if="unit"> {{ unit }}</small>
    </span>
    <span class="foot" :style="{ color: footColor ?? 'var(--text-muted)' }">{{ foot }}</span>
  </button>
</template>

<style scoped>
.kpi {
  background: var(--surface-card);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xs);
  padding: 15px 17px 13px;
  display: flex;
  flex-direction: column;
  gap: 5px;
  text-align: left;
  cursor: pointer;
  transition: var(--transition-colors);
}
.kpi:hover {
  border-color: var(--border-strong);
}
.kpi.active {
  border-color: var(--accent);
  box-shadow: var(--glow-sm);
}
.label {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-muted);
  font-weight: var(--weight-semibold);
}
.info {
  cursor: help;
  color: var(--text-faint);
  font-size: var(--fs-2xs);
}
.info:hover {
  color: var(--accent-text);
}
.value {
  font-family: var(--font-display);
  font-variant-numeric: tabular-nums;
  font-size: var(--fs-kpi);
  font-weight: var(--weight-semibold);
  line-height: 1.1;
  letter-spacing: var(--tracking-tight);
  color: var(--text-strong);
}
.value small {
  font-size: var(--fs-base);
  color: var(--text-faint);
  font-weight: var(--weight-medium);
}
.foot {
  font-size: var(--fs-2xs);
}
</style>
