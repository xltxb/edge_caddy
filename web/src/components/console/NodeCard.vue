<script setup lang="ts">
import VStatusPill from '@/components/base/VStatusPill.vue'
import SparkLine from './SparkLine.vue'
import type { EdgeNode } from '@/model'
import { fmtConns } from '@/utils/format'

defineProps<{ node: EdgeNode; selected?: boolean }>()
defineEmits<{ (e: 'select', id: string): void }>()
</script>

<template>
  <button
    class="card"
    :class="{ selected, drift: node.drift }"
    type="button"
    @click="$emit('select', node.id)"
  >
    <span class="head">
      <span class="ident">
        <span class="id">{{ node.id }}</span>
        <span class="city">{{ node.city }} · {{ node.vendor }}</span>
        <span class="line">{{ node.line }}</span>
      </span>
      <VStatusPill :status="node.status" />
    </span>

    <SparkLine :values="node.cpuSeries" :status="node.status" />

    <span class="metrics">
      <span>CPU <b>{{ node.cpu.toFixed(1) }}%</b></span>
      <span>内存 <b>{{ node.mem.toFixed(1) }}%</b></span>
      <span>连接 <b>{{ fmtConns(node.conns) }}</b></span>
    </span>

    <span v-if="node.drift" class="drift-note">
      版本 {{ node.cfgVersion }} · 未收到最近一次下发
    </span>
  </button>
</template>

<style scoped>
.card {
  border: 1px solid var(--border-subtle);
  border-radius: 12px;
  padding: 12px 13px 10px;
  display: flex;
  flex-direction: column;
  gap: 9px;
  background: var(--surface-card);
  cursor: pointer;
  text-align: left;
  box-shadow: var(--shadow-xs);
  transition: border-color var(--dur-fast) var(--ease-out);
}
.card:hover {
  border-color: var(--border-strong);
}
.card.selected {
  border-color: var(--accent);
  box-shadow: var(--glow-sm);
}
.card.drift {
  border-color: var(--warning);
}
.head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-2);
}
.ident {
  min-width: 0;
}
.id {
  display: block;
  font-family: var(--font-mono);
  font-size: var(--fs-sm);
  font-weight: var(--weight-semibold);
  color: var(--text-strong);
}
.city {
  display: block;
  font-size: var(--fs-2xs);
  color: var(--text-muted);
}
.line {
  display: block;
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-faint);
}
.metrics {
  display: flex;
  gap: 14px;
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--text-muted);
}
.metrics b {
  font-weight: var(--weight-semibold);
  color: var(--text-strong);
}
.drift-note {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--warning-text);
}
</style>
