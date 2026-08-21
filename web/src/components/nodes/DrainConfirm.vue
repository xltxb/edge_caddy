<script setup lang="ts">
import type { DrainStep } from '@/api/types'

defineProps<{
  nodeId: string
  conns: number
  busy: boolean
  /** 非 null 表示已经执行过，弹层切换到「结果」形态，由人自己关掉。 */
  result: DrainStep[] | null
}>()
defineEmits<{ (e: 'cancel'): void; (e: 'confirm'): void }>()

const STEP_LABEL: Record<DrainStep['step'], string> = {
  dns_removed: '摘除解析',
  conns_drained: '排空连接',
  tunnel_closed: '断开隧道',
}
</script>

<template>
  <div class="mask" @click.self="$emit('cancel')">
    <div class="modal" role="dialog" aria-modal="true" aria-label="下线节点">
      <div class="title">下线 {{ nodeId }}</div>

      <template v-if="!result">
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
          下线后该节点不再承载流量，不再接收配置下发，也不再报离线告警。
          之后可以在这台节点上「重新上线」撤销 —— 但解析不会跟着自动打开。
        </p>
        <div class="actions">
          <button class="ghost" type="button" @click="$emit('cancel')">取消</button>
          <button class="danger" type="button" :disabled="busy" @click="$emit('confirm')">
            {{ busy ? '下线中…' : '确认下线' }}
          </button>
        </div>
      </template>

      <!--
        结果形态。**detail 原样显示，不截断、不拼接。**

        每一步的 ok 说的是「这件事真的发生了」，不是「我这边的记录改成功了」——
        没配 DNS 服务商时摘解析就是 false，排空会被整个跳过（还在进水的池子排不
        干净）。而 detail 是人接下来那个决定的判据：「解析缓存未过期前仍可能有新
        连接进来」是「已排空」这句话的边界，不说的话人会据此关机。
      -->
      <template v-else>
        <ul class="result">
          <li v-for="s in result" :key="s.step" :class="{ bad: !s.ok }">
            <span class="mark">{{ s.ok ? '✓' : '✕' }}</span>
            <span class="body">
              <b>{{ STEP_LABEL[s.step] }}</b>
              <span class="detail">{{ s.detail || (s.ok ? '完成' : '未完成') }}</span>
            </span>
          </li>
        </ul>
        <div class="actions">
          <button class="ghost" type="button" @click="$emit('cancel')">知道了</button>
        </div>
      </template>
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
.result {
  list-style: none;
  margin: 0 0 var(--space-3);
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-2-5, 10px);
}
.result li {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
  font-size: var(--fs-xs);
  color: var(--text-body);
}
.result .mark {
  flex: none;
  width: 14px;
  color: var(--success-text, var(--accent));
}
.result li.bad .mark {
  color: var(--warning-text);
}
.result .body {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.result .detail {
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.7;
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
