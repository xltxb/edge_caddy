<script setup lang="ts">
import { computed } from 'vue'
import { fieldTone } from './fieldStyles'

const props = defineProps<{
  id?: string
  modelValue: unknown
  dirty: boolean
  invalid: boolean
  rows: number
}>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string[]): void }>()

// 多行文本在界面上是字符串，在数据里是数组。空行**保留**在编辑期间
// （否则光标会在你敲回车时跳走），等值比较那一侧才做规范化。
const text = computed(() =>
  Array.isArray(props.modelValue) ? (props.modelValue as string[]).join('\n') : '',
)

function onInput(e: Event): void {
  emit('update:modelValue', (e.target as HTMLTextAreaElement).value.split('\n'))
}
</script>

<template>
  <textarea
    :id="id"
    class="field"
    :rows="rows"
    :value="text"
    :style="fieldTone(dirty, invalid)"
    spellcheck="false"
    @input="onInput"
  />
</template>

<style scoped>
.field {
  width: 100%;
  padding: 7px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  color: var(--text-strong);
  font-size: var(--fs-xs);
  font-family: var(--font-mono);
  line-height: 1.6;
  resize: vertical;
  transition: var(--transition-colors);
}
.field:focus {
  outline: 2px solid var(--accent);
  outline-offset: 1px;
}
</style>
