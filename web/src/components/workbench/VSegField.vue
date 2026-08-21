<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  modelValue: unknown
  options: readonly (readonly [value: string, label: string])[]
  dirty: boolean
}>()
defineEmits<{ (e: 'update:modelValue', v: string): void }>()

/**
 * 当前值不在选项里（没配过，或后端给了个我们不认识的值）。
 *
 * 不说的话，这个控件看起来只是「没高亮」，与「已经选了但恰好没渲染出来」
 * 长得一样 —— 人无从判断此刻究竟什么在生效。
 */
const unset = computed(() => !props.options.some(([v]) => v === props.modelValue))
</script>

<template>
  <div class="wrap">
    <div class="seg" :class="{ dirty, unset }" role="radiogroup">
    <button
      v-for="[value, label] in options"
      :key="value"
      type="button"
      role="radio"
      :aria-checked="modelValue === value"
      class="opt"
      :class="{ on: modelValue === value }"
      @click="$emit('update:modelValue', value)"
    >
      {{ label }}
    </button>
    </div>
    <span v-if="unset" class="unset-note">
      未设置{{ modelValue === undefined || modelValue === '' ? '' : `（当前值 ${modelValue}）` }}
    </span>
  </div>
</template>

<style scoped>
.wrap {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.unset-note {
  font-size: var(--fs-2xs);
  color: var(--warning-text);
}
.seg.unset {
  border-style: dashed;
  border-color: var(--warning);
}
.seg {
  display: inline-flex;
  gap: 4px;
  padding: 2px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  transition: var(--transition-colors);
}
.seg.dirty {
  border-color: var(--accent);
  background: var(--accent-subtle);
}
.opt {
  padding: 4px 12px;
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: var(--text-muted);
  font-size: var(--fs-xs);
  font-family: var(--font-mono);
  cursor: pointer;
  transition: var(--transition-colors);
}
.opt:hover:not(.on) {
  color: var(--text-strong);
}
.opt.on {
  background: var(--accent);
  color: var(--text-on-accent);
  font-weight: var(--weight-semibold);
}
</style>
