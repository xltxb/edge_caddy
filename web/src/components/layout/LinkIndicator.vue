<script setup lang="ts">
import { computed } from 'vue'
import { linkLabel, useLinkStore } from '@/stores/link'

const link = useLinkStore()
const meta = computed(() => linkLabel(link.state))

const tone = computed(() => {
  switch (meta.value.tone) {
    case 'ok':
      return { dot: 'var(--success)', color: 'var(--text-muted)' }
    case 'warn':
      return { dot: 'var(--warning)', color: 'var(--warning-text)' }
    case 'danger':
      return { dot: 'var(--danger)', color: 'var(--danger-text)' }
  }
})

const pulsing = computed(() => link.state === 'connecting' || link.state === 'reconnecting')
</script>

<template>
  <!--
    实时通道状态是一等公民，不是装饰。断线时这里必须说出「已经不是实时的了」，
    否则用户对着一屏静止的旧数据以为一切正常 —— 与 ADR-0002 提醒的
    「界面不能给出兑现不了的承诺」是同一类错。
  -->
  <div class="link" :style="{ color: tone.color }" role="status" aria-live="polite">
    <span class="dot" :class="{ pulsing }" :style="{ background: tone.dot }" />
    <span class="text">{{ meta.text }}</span>
  </div>
</template>

<style scoped>
.link {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1-5);
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  white-space: nowrap;
}
.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex: none;
}
.dot.pulsing {
  animation: ec-pulse 1s ease-in-out infinite;
}
@media (prefers-reduced-motion: reduce) {
  .dot.pulsing {
    animation: none;
  }
}
</style>
