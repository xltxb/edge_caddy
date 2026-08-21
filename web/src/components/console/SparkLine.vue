<script setup lang="ts">
import { computed } from 'vue'
import type { NodeStatus } from '@/api/types'

const props = withDefaults(
  defineProps<{ values: number[]; status: NodeStatus; width?: number; height?: number }>(),
  { width: 208, height: 30 },
)

const stroke = computed(() =>
  props.status === 'down'
    ? 'var(--danger)'
    : props.status === 'warn'
      ? 'var(--warning)'
      : 'var(--azure-400)',
)

/**
 * 主控重启后 cpu_series 会空几十秒（契约 §4）。空数组时**留白**，
 * 不画一条误导性的零线，也不报错。
 */
const hasData = computed(() => props.values.length >= 2)

const geom = computed(() => {
  const { width: w, height: h, values } = props
  const max = Math.max(...values, 1)
  const pts = values.map<[number, number]>((v, i) => [
    (i / (values.length - 1)) * (w - 2) + 1,
    h - 3 - (v / max) * (h - 7),
  ])
  const line = pts.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(' ')
  const end = pts[pts.length - 1]!
  return {
    line,
    area: `${line} ${w - 1},${h} 1,${h}`,
    ex: end[0].toFixed(1),
    ey: end[1].toFixed(1),
  }
})
</script>

<template>
  <svg
    :width="'100%'"
    :height="height"
    :viewBox="`0 0 ${width} ${height}`"
    preserveAspectRatio="none"
    aria-hidden="true"
    class="spark"
  >
    <template v-if="hasData">
      <polygon :points="geom.area" :fill="stroke" opacity=".1" />
      <polyline
        :points="geom.line"
        fill="none"
        :stroke="stroke"
        stroke-width="1.6"
        stroke-linejoin="round"
        stroke-linecap="round"
      />
      <circle :cx="geom.ex" :cy="geom.ey" r="2.4" :fill="stroke" />
    </template>
  </svg>
</template>

<style scoped>
.spark {
  display: block;
}
</style>
