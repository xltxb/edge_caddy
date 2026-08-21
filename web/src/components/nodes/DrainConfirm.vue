<script setup lang="ts">
defineProps<{ nodeId: string; conns: number; busy: boolean }>()
defineEmits<{ (e: 'cancel'): void; (e: 'confirm'): void }>()
</script>

<template>
  <div class="mask" @click.self="$emit('cancel')">
    <div class="modal" role="dialog" aria-modal="true" aria-label="下线节点">
      <div class="title">下线 {{ nodeId }}</div>
      <!--
        契约要求 drain 必须带 confirm，防止误点。所以这个弹层不是装饰性的
        「你确定吗」——它是那个 confirm 的来源，而且要说清会发生什么。
      -->
      <ol class="steps">
        <li>从所有线路的 DNS 解析里摘除该节点</li>
        <li>等待现有 {{ conns }} 条连接自然结束</li>
        <li>关闭它与主控之间的 gRPC 隧道</li>
      </ol>
      <p class="note">
        下线后该节点不再承载流量，也不再接收配置下发。要恢复需要重新接入。
      </p>
      <div class="actions">
        <button class="ghost" type="button" @click="$emit('cancel')">取消</button>
        <button class="danger" type="button" :disabled="busy" @click="$emit('confirm')">
          {{ busy ? '下线中…' : '确认下线' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mask {
  position: fixed;
  inset: 0;
  background: var(--surface-overlay);
  z-index: var(--z-modal);
  display: grid;
  place-items: center;
  padding: var(--space-6);
}
.modal {
  width: min(420px, 100%);
  background: var(--surface-raised);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xl);
  padding: var(--space-5);
}
.title {
  font-family: var(--font-mono);
  font-size: var(--fs-base);
  font-weight: var(--weight-bold);
  color: var(--text-strong);
  margin-bottom: var(--space-3);
}
.steps {
  margin: 0 0 var(--space-3);
  padding-left: 20px;
  font-size: var(--fs-xs);
  color: var(--text-body);
  line-height: 1.9;
}
.note {
  margin: 0 0 var(--space-4);
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.6;
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
}
.ghost {
  padding: 6px 14px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-xs);
  cursor: pointer;
}
.danger {
  padding: 6px 14px;
  border: 1px solid var(--danger);
  border-radius: var(--radius-sm);
  background: var(--danger);
  color: #fff;
  font-size: var(--fs-xs);
  font-weight: var(--weight-semibold);
  cursor: pointer;
}
</style>
