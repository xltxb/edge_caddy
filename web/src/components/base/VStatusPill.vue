<script setup lang="ts">
import { computed } from 'vue'
import type { NodeStatus } from '@/api/types'
import { statusMeta } from '@/utils/format'

const props = defineProps<{ status: NodeStatus }>()
const meta = computed(() => statusMeta(props.status))
</script>

<template>
  <span class="pill" :style="{ background: meta.bg, color: meta.color }">
    <span class="dot" :style="{ background: meta.dot }" />
    {{ meta.text }}
  </span>
</template>

<style scoped>
/* 色 + 圆点 + 文字三重编码：色盲用户靠文字，灰度打印靠文字，都不丢信息。 */
.pill {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1-5);
  padding: var(--space-0-5) 10px;
  border-radius: var(--radius-full);
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  font-weight: var(--weight-semibold);
  white-space: nowrap;
}
.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex: none;
}
</style>
