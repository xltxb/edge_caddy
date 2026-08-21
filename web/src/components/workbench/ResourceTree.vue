<script setup lang="ts">
import { computed } from 'vue'
import type { ResourceItem } from '@/stores/config'

const props = defineProps<{ items: ResourceItem[]; selected: string }>()
defineEmits<{ (e: 'select', key: string): void }>()

const groups = computed(() => {
  const order = ['反代路由', '访问规则', '全局策略']
  return order
    .map((g) => ({ group: g, items: props.items.filter((i) => i.group === g) }))
    .filter((g) => g.items.length > 0)
})
</script>

<template>
  <nav class="tree">
    <template v-for="g in groups" :key="g.group">
      <div class="group">{{ g.group }}</div>
      <button
        v-for="it in g.items"
        :key="it.key"
        type="button"
        class="item"
        :class="{ on: selected === it.key }"
        @click="$emit('select', it.key)"
      >
        <span class="label">{{ it.label }}</span>
        <!-- 蓝点 = 有未下发改动。它必须与「待下发」计数同源，否则就是在撒谎。 -->
        <span v-if="it.dirty" class="dot" :title="`${it.changes} 处未下发改动`" />
        <span v-if="it.isNew" class="new">新</span>
      </button>
    </template>
  </nav>
</template>

<style scoped>
.tree {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: var(--space-3);
  overflow-y: auto;
}
.group {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-muted);
  font-weight: var(--weight-semibold);
  padding: var(--space-3) var(--space-2) var(--space-1);
}
.group:first-child {
  padding-top: 0;
}
.item {
  display: flex;
  align-items: center;
  gap: var(--space-1-5);
  width: 100%;
  padding: 6px 9px;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-body);
  font-size: var(--fs-xs);
  font-family: var(--font-mono);
  text-align: left;
  cursor: pointer;
  transition: var(--transition-colors);
}
.item:hover {
  background: var(--surface-sunken);
}
.item.on {
  background: var(--accent-subtle);
  color: var(--accent-text);
  font-weight: var(--weight-semibold);
}
.label {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--accent);
  flex: none;
}
.new {
  font-size: var(--fs-micro);
  padding: 0 5px;
  border-radius: var(--radius-full);
  background: var(--success-subtle);
  color: var(--success-text);
  flex: none;
}
</style>
