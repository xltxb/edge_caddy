<script setup lang="ts">
defineProps<{ modelValue: unknown; dirty: boolean; id?: string; disabled?: boolean }>()
defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()
</script>

<template>
  <button
    :id="id"
    type="button"
    class="track"
    role="switch"
    :aria-checked="modelValue === true"
    :disabled="disabled"
    :class="{ on: modelValue === true, dirty }"
    @click="$emit('update:modelValue', modelValue !== true)"
  >
    <span class="knob" />
  </button>
</template>

<style scoped>
.track:disabled {
  cursor: not-allowed;
  opacity: 0.45;
}
.track {
  width: 34px;
  height: 18px;
  flex: none;
  padding: 0;
  border: 0;
  border-radius: var(--radius-full);
  background: var(--border-strong);
  cursor: pointer;
  position: relative;
  transition: var(--transition-colors);
}
.track.on {
  background: var(--success);
}
.track.dirty {
  box-shadow: 0 0 0 2px var(--accent-subtle-border);
}
.knob {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: var(--surface-card);
  transition: transform var(--dur-fast) var(--ease-out);
}
.track.on .knob {
  transform: translateX(16px);
}
</style>
