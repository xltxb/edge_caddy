<script setup lang="ts">
import { fieldTone } from './fieldStyles'

const props = defineProps<{
  id?: string
  modelValue: unknown
  dirty: boolean
  invalid: boolean
  width?: string
  numeric?: boolean
  disabled?: boolean
}>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string | number): void }>()

/**
 * **空框不能变成 0。**
 *
 * 原先是 `Number(raw)` —— 而 `Number('') === 0`。人把一个数字框清空想重新
 * 输入，草稿在 400ms 后就写进了一个 **0**：那不是「没填」，是一个合法的数字。
 *
 * 灰度上撞到的就是这个：一条限流规则的 `window_s` 成了 0，
 * 下发时被后端 1002 拒（「时间窗口要大于 0 秒」）。而清空那一刻界面上是红的、
 * 工作台的下发按钮也禁着 —— **人只是离开了那一页**，
 * 草稿留在服务端（全局可见），几天后另一个人点顶栏的下发才撞上。
 *
 * > 一个空框和一个填了 0 的框，在草稿里长得一模一样 ——
 * > 而它们的意思是「还没填」和「我要求 0 秒」。
 *
 * 非数字（`Number('abc')` 是 NaN）保持原样：NaN 进 JSON 会变成 null，
 * 而原样留着的话，人至少看得见自己输入了什么，校验也报得出来。
 */
function onInput(e: Event): void {
  const raw = (e.target as HTMLInputElement).value
  if (!props.numeric) return emit('update:modelValue', raw)
  if (raw.trim() === '') return emit('update:modelValue', '')
  const n = Number(raw)
  emit('update:modelValue', Number.isNaN(n) ? raw : n)
}
</script>

<template>
  <input
    :id="id"
    class="field"
    type="text"
    :inputmode="numeric ? 'numeric' : undefined"
    :value="modelValue ?? ''"
    :disabled="disabled"
    :style="{ ...fieldTone(dirty, invalid), width: width ?? '100%' }"
    @input="onInput"
  />
</template>

<style scoped>
.field:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
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
