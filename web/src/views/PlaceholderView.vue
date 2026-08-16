<script setup lang="ts">
import { useRoute } from 'vue-router'
const route = useRoute()
</script>

<template>
  <!-- 占位页而不是死链：死链让人以为坏了，占位页告诉人这里还没做，
       并指出对应的工单编号。 -->
  <div class="ph">
    <div class="title">{{ route.meta.title ?? '该页面' }}</div>
    <p class="body">这一块还没有实现。</p>
    <p v-if="route.meta.issue" class="mono issue">
      对应工单 #{{ route.meta.issue }}
    </p>
  </div>
</template>

<style scoped>
.ph { padding: 56px 24px; text-align: center; color: var(--text-muted); }
.title { font-size: 16px; font-weight: 700; color: var(--text-strong); margin-bottom: 8px; }
.body { margin: 0 0 6px; font-size: 13.5px; }
.issue { font-size: 11.5px; color: var(--text-faint); }
</style>
