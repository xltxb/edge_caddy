<script setup lang="ts">
defineProps<{
  modelValue: unknown
  options: readonly (readonly [value: string, label: string])[]
  dirty: boolean
}>()
defineEmits<{ (e: 'update:modelValue', v: string): void }>()
</script>

<template>
  <div class="seg" :class="{ dirty }" role="radiogroup">
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
</template>

<style scoped>
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
