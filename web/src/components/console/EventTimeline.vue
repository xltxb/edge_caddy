<script setup lang="ts">
import type { ConsoleEvent } from '@/model'
import { eventColor, fmtClock } from '@/utils/format'

defineProps<{ events: ConsoleEvent[] }>()
</script>

<template>
  <ol class="timeline">
    <li v-if="!events.length" class="empty">还没有事件。节点上报后会出现在这里。</li>
    <li v-for="e in events" :key="e.id" class="row">
      <span class="dot" :style="{ background: eventColor(e.kind) }" />
      <span class="body">
        <span class="meta">
          <span class="time">{{ fmtClock(e.at) }}</span>
          <span class="node">{{ e.node }}</span>
        </span>
        <span class="msg">{{ e.msg }}</span>
      </span>
    </li>
  </ol>
</template>

<style scoped>
.timeline {
  list-style: none;
  margin: 0;
  padding: var(--space-3) var(--space-4);
  display: flex;
  flex-direction: column;
  gap: 11px;
  max-height: 520px;
  overflow-y: auto;
}
.empty {
  padding: 30px 0;
  text-align: center;
  color: var(--text-muted);
  font-size: var(--fs-sm);
}
.row {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
}
.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex: none;
  margin-top: 6px;
}
.body {
  min-width: 0;
}
.meta {
  display: flex;
  gap: var(--space-2);
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-faint);
}
.node {
  color: var(--text-muted);
}
.msg {
  display: block;
  font-size: var(--fs-xs);
  color: var(--text-body);
  line-height: 1.55;
}
</style>
