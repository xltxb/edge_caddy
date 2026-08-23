<script setup lang="ts">
import { isInUse } from '@/certs/orphan'
import type { CertWire } from '@/api/types'

defineProps<{
  cert: CertWire
  busy: boolean
  /**
   * 被 `2001` 拒了 —— 后端说它还在服务某些站点。**原样显示那句话**：
   * 它列出了在服务哪些域名，而那是人决定要不要强删的唯一依据。
   */
  refused: string | null
  /** 非 null 表示删成了，弹层切到结果态 —— detail 要人自己关掉。 */
  result: string | null
}>()
defineEmits<{ (e: 'cancel'): void; (e: 'confirm', force: boolean): void }>()
</script>

<template>
  <div class="mask" @click.self="$emit('cancel')">
    <div class="modal" role="dialog" aria-modal="true" aria-label="删除证书">
      <div class="title">删除 {{ cert.domain }}</div>

      <!-- ① 结果态 —— detail 原样显示，由人自己关掉 -->
      <template v-if="result !== null">
        <p class="detail">{{ result }}</p>
        <div class="actions">
          <button class="primary" type="button" @click="$emit('cancel')">知道了</button>
        </div>
      </template>

      <!--
        ② 被拒态。**这不是「你确定吗」，是后端给的一个事实 + 一条出路。**

        它列出了这张证书在服务哪些域名 —— 那是人决定要不要强删的唯一依据，
        原样显示，不改写不截断。

        逃生口是有意留的（契约 §9）：一张 `*.example.com` 可能覆盖二十条路由，
        要求「先把它们都删掉」等于要求不可能的事，**而人会绕开 —— 直接进数据库
        删。那之后下发不会被触发，节点上那张证书会一直留着。**
        一道逼人绕开的门比没有门更糟。
      -->
      <template v-else-if="refused">
        <p class="refused">{{ refused }}</p>
        <p class="note danger">
          强制删除会让上面那些站点<b>在按下按钮的那一刻</b>就坏掉 ——
          这跟证书过期不同，过期还有几天窗口。
        </p>
        <div class="actions">
          <button class="ghost" type="button" @click="$emit('cancel')">取消</button>
          <button class="danger" type="button" :disabled="busy" @click="$emit('confirm', true)">
            {{ busy ? '删除中…' : '仍然删除' }}
          </button>
        </div>
      </template>

      <!-- ③ 确认态 -->
      <template v-else>
        <!--
          **删掉之后的边界要说在前面。**

          `load_pem` 是全量替换的，只有下一次下发才会把它从节点上摘掉 ——
          **契约 §9**：后端在删除时会顺带触发一次，所以正常路径下「已删除」是真的。
          但那一步可能失败（`3001`），那时 detail 会说「已从库中删除，
          但节点上仍在服务它」——所以这里不预先承诺，由结果那句话来说。
        -->
        <p class="lead">
          删掉之后主控不再持有它，并从各节点上摘掉。
          <span v-if="!isInUse(cert.covers)">
            这张证书<b>没有路由在用</b>，删它不会影响任何站点。
          </span>
        </p>
        <dl class="facts">
          <dt>覆盖的域名</dt>
          <dd class="mono">{{ cert.domains?.join('、') || '—' }}</dd>
          <dt>正在服务</dt>
          <!--
            三种值三个意思（契约 §9）：`null` 是「算不出来」，**不能说成
            「没有」** —— 那会让人以为删它是安全的，而它可能还在服务二十条路由。
          -->
          <dd v-if="cert.covers === null" class="warn">
            算不出来（读不到路由清单）—— 这一栏这次说不了话
          </dd>
          <dd v-else-if="cert.covers.length === 0" class="muted">没有路由在用它</dd>
          <dd v-else class="mono">{{ cert.covers.join('、') }}</dd>
        </dl>
        <div class="actions">
          <button class="ghost" type="button" @click="$emit('cancel')">取消</button>
          <button class="danger" type="button" :disabled="busy" @click="$emit('confirm', false)">
            {{ busy ? '删除中…' : '删除' }}
          </button>
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
  width: min(460px, 100%);
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
.lead {
  margin: 0 0 var(--space-4);
  font-size: var(--fs-xs);
  color: var(--text-muted);
  line-height: 1.7;
}
.lead b {
  color: var(--text-body);
  font-weight: var(--weight-semibold);
}
.facts {
  margin: 0 0 var(--space-4);
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: var(--space-1) var(--space-3);
  font-size: var(--fs-2xs);
}
.facts dt {
  color: var(--text-faint);
}
.facts dd {
  margin: 0;
  color: var(--text-body);
  word-break: break-all;
}
.facts .warn {
  color: var(--warning-text);
}
.facts .muted {
  color: var(--text-faint);
}
.refused {
  margin: 0 0 var(--space-3);
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--surface-sunken);
  border: 1px solid var(--border-subtle);
  font-size: var(--fs-2xs);
  line-height: 1.8;
  color: var(--text-body);
}
.note {
  margin: 0 0 var(--space-4);
  font-size: var(--fs-2xs);
  line-height: 1.7;
  color: var(--text-muted);
}
.note.danger {
  color: var(--danger-text);
}
.detail {
  margin: 0 0 var(--space-4);
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--surface-sunken);
  border: 1px solid var(--border-subtle);
  font-size: var(--fs-2xs);
  line-height: 1.8;
  color: var(--text-body);
  white-space: pre-wrap;
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
}
.ghost {
  padding: 5px 12px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-micro);
  cursor: pointer;
}
.primary {
  padding: 6px 14px;
  border: 1px solid var(--accent);
  border-radius: var(--radius-sm);
  background: var(--accent);
  color: var(--text-on-accent);
  font-size: var(--fs-xs);
  font-weight: var(--weight-semibold);
  cursor: pointer;
}
/*
 * **限定在 .actions 下。**
 *
 * 不限定的话它会命中 `<p class="note danger">` —— 那一段拿到按钮的粉红底，
 * 而 `.note.danger` 特异性更高又把文字设成同一个粉红：**粉底粉字，一个字
 * 看不见**。而全套 9 步检查一条都不红：类型、单测、e2e、文案扫描，
 * 没有一样看得见颜色。
 *
 * 这是起 dev server 看了一眼才发现的 —— **有些东西只有眼睛能验**。
 */
.actions .danger {
  padding: 6px 14px;
  border: 1px solid var(--danger-border, var(--danger-text));
  border-radius: var(--radius-sm);
  background: var(--danger-text);
  color: var(--text-on-accent);
  font-size: var(--fs-xs);
  font-weight: var(--weight-semibold);
  cursor: pointer;
}
.actions .danger:disabled {
  opacity: 0.6;
  cursor: default;
}
</style>
