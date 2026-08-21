<script setup lang="ts">
defineProps<{ title: string }>()
</script>

<template>
  <section class="panel">
    <div class="title">{{ title }}</div>
    <p class="body">
      这一页还没实现。骨架阶段先打通「集群总览」一条竖线，其余页面按 PRD 的三个高频场景
      依次补上：改配置 → 排障 → 其余。
    </p>
    <p class="note">导航里没有死链，这里是占位不是错误。</p>
  </section>
</template>

<style scoped>
.panel {
  background: var(--surface-card);
  border: 1px dashed var(--border-default);
  border-radius: var(--radius-lg);
  padding: 48px var(--space-6);
  text-align: center;
}
.title {
  font-family: var(--font-display);
  font-size: var(--fs-lg);
  font-weight: var(--weight-bold);
  color: var(--text-strong);
  margin-bottom: var(--space-2);
}
.body {
  font-size: var(--fs-sm);
  color: var(--text-muted);
  max-width: 46ch;
  margin: 0 auto var(--space-2);
  line-height: var(--leading-relaxed);
}
.note {
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--text-faint);
  margin: 0;
}
</style>
