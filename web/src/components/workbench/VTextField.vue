<script setup lang="ts">
import { fieldTone } from './fieldStyles'

const props = defineProps<{
  id?: string
  modelValue: unknown
  dirty: boolean
  invalid: boolean
  width?: string
  numeric?: boolean
}>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string | number): void }>()

function onInput(e: Event): void {
  const raw = (e.target as HTMLInputElement).value
  emit('update:modelValue', props.numeric ? Number(raw) : raw)
}
</script>

<template>
  <input
    :id="id"
    class="field"
    type="text"
    :inputmode="numeric ? 'numeric' : undefined"
    :value="modelValue ?? ''"
    :style="{ ...fieldTone(dirty, invalid), width: width ?? '100%' }"
    @input="onInput"
  />
</template>

<style scoped>
.field {
  padding: 7px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  color: var(--text-strong);
  font-size: var(--fs-xs);
  font-family: var(--font-mono);
  transition: var(--transition-colors);
}
.field:focus {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}
</style>
