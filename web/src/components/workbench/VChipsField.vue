<script setup lang="ts">
const props = defineProps<{
  modelValue: unknown
  choices: string[]
  dirty: boolean
}>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string[]): void }>()

const selected = () => (Array.isArray(props.modelValue) ? (props.modelValue as string[]) : [])

function toggle(v: string): void {
  const cur = selected()
  emit('update:modelValue', cur.includes(v) ? cur.filter((x) => x !== v) : [...cur, v])
}
</script>

<template>
  <div class="chips">
    <button
      v-for="c in choices"
      :key="c"
      type="button"
      class="chip"
      :class="{ on: selected().includes(c), dirty }"
      :aria-pressed="selected().includes(c)"
      @click="toggle(c)"
    >
      {{ c }}
    </button>
    <span v-if="!choices.length" class="none">还没有可绑定的域名</span>
  </div>
</template>

<style scoped>
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-1-5);
}
.chip {
  padding: 3px 11px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-full);
  background: var(--surface-card);
  color: var(--text-muted);
  font-size: var(--fs-2xs);
  font-family: var(--font-mono);
  cursor: pointer;
  transition: var(--transition-colors);
}
.chip:hover {
  border-color: var(--border-strong);
}
.chip.on {
  background: var(--accent-subtle);
  border-color: var(--accent);
  color: var(--accent-text);
  font-weight: var(--weight-semibold);
}
.none {
  font-size: var(--fs-2xs);
  color: var(--text-faint);
}
</style>
