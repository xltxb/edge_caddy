<script setup lang="ts">
import type { DeployResultWire } from '@/api/types'

defineProps<{ rows: DeployResultWire[]; nodeLabel?: (id: string) => string }>()

const TONE: Record<string, { color: string; dot: string; text: string }> = {
  wait: { color: 'var(--text-faint)', dot: 'var(--border-strong)', text: '排队中' },
  run: { color: 'var(--accent-text)', dot: 'var(--accent)', text: '热重载中' },
  ok: { color: 'var(--success-text)', dot: 'var(--success)', text: '已接受' },
  fail: { color: 'var(--danger-text)', dot: 'var(--danger)', text: '失败' },
}
</script>

<template>
  <ul class="rows">
    <li v-for="r in rows" :key="r.node" class="row">
      <span class="dot" :class="{ pulse: r.state === 'run' }" :style="{ background: TONE[r.state]!.dot }" />
      <span class="id">{{ r.node }}</span>
      <span v-if="nodeLabel" class="city">{{ nodeLabel(r.node) }}</span>
      <span class="detail" :style="{ color: TONE[r.state]!.color }">
        {{ r.detail }}
        <!--
          ADR-0005：节点未回应才重试，Caddy 拒绝配置不重试。
          这一行还会不会再动，决定了人该等还是该去改配置。
        -->
        <em v-if="r.state === 'fail' && r.retrying" class="retry">· 重试中</em>
        <em v-else-if="r.state === 'fail'" class="final">· 需人工处理</em>
      </span>
    </li>
  </ul>
</template>

<style scoped>
.rows {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 7px;
}
.row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
}
.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex: none;
}
.dot.pulse {
  animation: ec-pulse 1s ease-in-out infinite;
}
@media (prefers-reduced-motion: reduce) {
  .dot.pulse {
    animation: none;
  }
}
.id {
  color: var(--text-strong);
  min-width: 92px;
}
.city {
  color: var(--text-faint);
  flex: 1;
}
.detail {
  margin-left: auto;
  white-space: nowrap;
}
.retry,
.final {
  font-style: normal;
  color: var(--text-faint);
}
</style>
