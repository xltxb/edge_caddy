<script setup lang="ts">
defineProps<{
  nodeId: string
  busy: boolean
  /** 非 null 表示已经删了，弹层切到结果态 —— detail 要人自己关掉。 */
  result: string | null
}>()
defineEmits<{ (e: 'cancel'): void; (e: 'confirm'): void }>()
</script>

<template>
  <div class="mask" @click.self="$emit('cancel')">
    <div class="modal" role="dialog" aria-modal="true" aria-label="删除节点">
      <div class="title">删除 {{ nodeId }}</div>

      <template v-if="result === null">
        <!--
          这个弹层说的是**这一下会做什么、不会做什么**，不是「你确定吗」。
          「必须先下线」那条前提在按钮上就拦住了（canDelete），走到这里的
          一定是已下线的节点 —— 所以这里不再重复它，只说删除的边界。
        -->
        <p class="lead">
          只删主控这边的记录。<b>那台机器上的 Agent 与 Caddy 不受影响</b> ——
          它们还在跑，还在监听 80/443，还在用最后一次拿到的配置服务。
        </p>
        <div class="cols">
          <div>
            <div class="col-title">跟着删</div>
            <ul>
              <li>解析权重</li>
              <li>证书绑定</li>
              <li>Agent 日志</li>
            </ul>
            <p class="small">它们是关于这台机器<b>此刻的安排</b>，机器没了就没有意义。</p>
          </div>
          <div>
            <div class="col-title">留下</div>
            <ul>
              <li>下发记录</li>
              <li>事件</li>
              <li>审计日志</li>
            </ul>
            <p class="small">那是<b>历史</b>。删一台机器不该让过去发生过的事从记录里消失。</p>
          </div>
        </div>
        <div class="actions">
          <button class="ghost" type="button" @click="$emit('cancel')">取消</button>
          <button class="danger" type="button" :disabled="busy" @click="$emit('confirm')">
            {{ busy ? '删除中…' : '确认删除' }}
          </button>
        </div>
      </template>

      <!--
        结果态。**detail 原样显示，不改写、不截断。**

        它说的是这个操作只做了一半：记录没了，那台机器还在跑。人点完删除会以为
        干净了 —— 而**一个只做了一半的操作，必须自己说出另一半是什么**。
        那条 uninstall 命令是另一半的全部内容，所以它要能被复制走。
      -->
      <template v-else>
        <p class="result">{{ result }}</p>
        <div class="actions">
          <button class="primary" type="button" @click="$emit('cancel')">知道了</button>
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
  color: var(--warning-text);
  font-weight: var(--weight-semibold);
}
.cols {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-4);
  margin-bottom: var(--space-4);
}
.col-title {
  font-size: var(--fs-2xs);
  font-weight: var(--weight-semibold);
  color: var(--text-body);
  margin-bottom: var(--space-1);
}
.cols ul {
  margin: 0 0 var(--space-1);
  padding-left: 16px;
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.8;
}
.small {
  margin: 0;
  font-size: var(--fs-micro);
  color: var(--text-faint);
  line-height: 1.6;
}
.small b {
  color: var(--text-muted);
  font-weight: var(--weight-semibold);
}
.result {
  margin: 0 0 var(--space-4);
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--surface-sunken);
  border: 1px solid var(--border-subtle);
  font-size: var(--fs-2xs);
  line-height: 1.8;
  color: var(--text-body);
  white-space: pre-wrap;
  word-break: break-word;
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
.danger {
  padding: 6px 14px;
  border: 1px solid var(--danger-border, var(--danger-text));
  border-radius: var(--radius-sm);
  background: var(--danger-text);
  color: var(--text-on-accent);
  font-size: var(--fs-xs);
  font-weight: var(--weight-semibold);
  cursor: pointer;
}
.danger:disabled {
  opacity: 0.6;
  cursor: default;
}
</style>
