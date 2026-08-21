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
      <!--
        消息在上、时间与节点在下。人扫这一栏是在找「出了什么事」，
        不是在找「几点」—— 把时间戳放在第一行会让每一条都以一串数字开头。
      -->
      <span class="body">
        <span class="msg">{{ e.msg }}</span>
        <span class="meta">
          <span class="time">{{ fmtClock(e.at) }}</span>
          <span class="sep">·</span>
          <span class="node">{{ e.node }}</span>
        </span>
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
  gap: var(--space-1-5);
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-faint);
  margin-top: 2px;
}
.sep {
  color: var(--text-faint);
}
.node {
  color: var(--text-muted);
}
.msg {
  display: block;
  font-size: var(--fs-xs);
  color: var(--text-strong);
  line-height: 1.55;
}
</style>
