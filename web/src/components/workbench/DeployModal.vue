<script setup lang="ts">
import { computed, ref } from 'vue'
import DeployProgress from './DeployProgress.vue'
import JsonDiff from './JsonDiff.vue'
import { useDeployStore } from '@/stores/deploy'
import { useNodesStore } from '@/stores/nodes'

const props = defineProps<{ resKeys: string[]; labelOf: (key: string) => string }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'confirmed'): void }>()

const deploy = useDeployStore()
const nodes = useNodesStore()

/**
 * 权威 diff 默认折叠。
 *
 * 设计稿的确认弹层只有变更摘要，没有 diff。ADR-0007 把权威 diff 放在这里——
 * 右栏那份是可读表示，不是将要下发的字节，所以「所见即所发」这个性质
 * **只在这个弹层里成立**。默认折叠是为了不打断「看一眼摘要就点确认」的常路。
 */
const diffOpen = ref(false)

const summary = computed(() =>
  props.resKeys.map((k) => ({ key: k, label: props.labelOf(k) })),
)

const troubled = computed(
  () => deploy.preview?.targets.filter((t) => t.status !== 'ok').length ?? 0,
)

const nodeCity = (id: string) => nodes.byId.get(id)?.city ?? ''

async function onConfirm(): Promise<void> {
  await deploy.confirm(props.resKeys)
  emit('confirmed')
}
</script>

<template>
  <div class="mask" @click.self="emit('close')">
    <div class="modal" role="dialog" aria-modal="true" aria-label="校验并下发">
      <header class="head">
        <div class="title">校验并下发</div>
        <!--
          只显示基线。新版本号在下发那一刻才生成，这里编一个必然与下发记录对不上。
        -->
        <div v-if="deploy.preview" class="cfg">
          基线 {{ deploy.preview.baseline }} → 新版本（下发时生成）
        </div>
      </header>

      <!-- 预览中 -->
      <div v-if="deploy.phase === 'previewing'" class="body center">正在渲染并校验…</div>

      <!-- 确认 -->
      <div v-else-if="deploy.phase === 'confirm' && deploy.preview" class="body">
        <div class="section-label">本次变更</div>
        <ul class="summary">
          <li v-for="s in summary" :key="s.key">
            <span class="res">{{ s.label }}</span>
            <span class="cnt">{{ deploy.fieldErrors[s.key] ? '有校验错误' : '待下发' }}</span>
          </li>
        </ul>

        <div v-if="!deploy.preview.validation.ok" class="errors">
          <div class="err-title">校验未通过，不会触达任何节点</div>
          <ul>
            <li v-for="(e, i) in deploy.preview.validation.errors" :key="i">
              <code>{{ e.res_key }}</code> · <code>{{ e.field }}</code> — {{ e.reason }}
            </li>
          </ul>
        </div>

        <!--
          after 为 null = 校验没过，主控没渲染出可下发的配置。**不能拿去 diff**：
          当成空串会让整份配置显示成全红删除，读起来像「这次下发会删光一切」，
          比不显示 diff 误导得多。
          before 为 null = 基线自己渲染不出来，这时喂空串给 diff 是**对的**
          （整份显示为新增），但要在下面说明原因。
        -->
        <template v-if="deploy.preview.after !== null">
          <button class="disclose" type="button" @click="diffOpen = !diffOpen">
            {{ diffOpen ? '收起完整变更' : '查看完整变更（主控渲染）' }}
          </button>
          <div v-if="diffOpen" class="diffbox">
            <JsonDiff :before="deploy.preview.before ?? ''" :after="deploy.preview.after" />
          </div>
          <p v-if="deploy.preview.before === null" class="note">
            当前基线渲染不出来，下面整份显示为新增 —— 这不表示配置真的都是新加的。
          </p>
        </template>
        <p v-else class="note">
          校验未通过，主控没有渲染出可下发的配置，因此没有可比对的变更。
        </p>

        <div class="section-label">目标节点</div>
        <ul class="targets">
          <li v-for="t in deploy.preview.targets" :key="t.id">
            <span class="tid">{{ t.id }}</span>
            <span class="tcity">{{ nodeCity(t.id) }}</span>
            <span class="tst" :class="t.status">{{ t.status }}</span>
          </li>
        </ul>
      </div>

      <!-- 进行中 / 完成 -->
      <div v-else-if="deploy.current" class="body">
        <div class="section-label">
          热重载进度 {{ deploy.doneCount }}/{{ deploy.current.rows.length }}
          <!--
            还在重试的行会继续变（ADR-0005），所以「6/6」并不等于「结束了」。
            不说出来的话，人会以为已经落定然后关掉弹层。
          -->
          <span v-if="deploy.retryingCount" class="retrying">
            · {{ deploy.retryingCount }} 个节点重试中
          </span>
        </div>
        <DeployProgress :rows="deploy.current.rows" :node-label="nodeCity" />
      </div>

      <footer class="foot">
        <div class="notes">
          <!-- 契约 §7.1 / ADR-0007 的补充：证书段不在 diff 里，不标就是给一个兑现不了的承诺 -->
          <p v-if="deploy.phase === 'confirm'" class="note">
            证书段由主控自动附加，不在此 diff 中。
          </p>
          <p v-if="deploy.phase === 'confirm' && troubled" class="note warn">
            {{ troubled }} 个节点通道异常，失败后会进重试队列。
          </p>
          <!--
            state=ok 的含义是「Caddy 接受了这份配置」，不是「流量正在被服务」。
            实测过端口被占用时 Caddy 返回 200、日志无 error，而流量进不来。
          -->
          <p v-if="deploy.phase === 'done'" class="note">
            {{ deploy.okCount }} 个节点已接受配置，{{ deploy.failCount }} 个失败。
            「已接受」指 Caddy 收下了这份配置，不代表流量已经在走。
          </p>
        </div>

        <div class="actions">
          <button class="ghost" type="button" @click="emit('close')">
            {{ deploy.phase === 'done' ? '关闭' : '取消' }}
          </button>
          <button
            v-if="deploy.phase === 'confirm'"
            class="primary"
            type="button"
            :disabled="!deploy.canDeploy"
            @click="onConfirm"
          >
            确认下发到 {{ deploy.preview?.targets.length ?? 0 }} 个节点
          </button>
        </div>
      </footer>
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
  width: min(720px, 100%);
  max-height: 86vh;
  display: flex;
  flex-direction: column;
  background: var(--surface-raised);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xl);
  overflow: hidden;
}
.head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  padding: var(--space-4) var(--space-5);
  border-bottom: 1px solid var(--border-subtle);
}
.title {
  font-size: var(--fs-base);
  font-weight: var(--weight-bold);
  color: var(--text-strong);
}
.cfg {
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  color: var(--text-muted);
}
.body {
  padding: var(--space-4) var(--space-5);
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.body.center {
  align-items: center;
  color: var(--text-muted);
  font-size: var(--fs-sm);
  padding: 40px;
}
.retrying {
  color: var(--warning-text);
  font-weight: var(--weight-regular);
}
.section-label {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-muted);
  font-weight: var(--weight-semibold);
}
.summary,
.targets {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-1-5);
}
.summary li,
.targets li {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
}
.res,
.tid {
  color: var(--text-strong);
  min-width: 160px;
}
.tcity {
  color: var(--text-faint);
  flex: 1;
}
.cnt,
.tst {
  margin-left: auto;
  color: var(--text-muted);
}
.tst.warn {
  color: var(--warning-text);
}
.tst.down {
  color: var(--danger-text);
}
.errors {
  border: 1px solid var(--danger);
  background: var(--danger-subtle);
  border-radius: var(--radius-sm);
  padding: var(--space-3);
  font-size: var(--fs-2xs);
  color: var(--danger-text);
}
.err-title {
  font-weight: var(--weight-semibold);
  margin-bottom: var(--space-1-5);
}
.errors ul {
  margin: 0;
  padding-left: 18px;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.disclose {
  align-self: flex-start;
  padding: 4px 11px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-2xs);
  cursor: pointer;
}
.disclose:hover {
  background: var(--surface-sunken);
}
.diffbox {
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  max-height: 320px;
  overflow: auto;
  background: var(--surface-sunken);
}
.foot {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: var(--space-4);
  padding: var(--space-3) var(--space-5) var(--space-4);
  border-top: 1px solid var(--border-subtle);
}
.notes {
  min-width: 0;
}
.note {
  margin: 0;
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.6;
}
.note.warn {
  color: var(--warning-text);
}
.actions {
  display: flex;
  gap: var(--space-2);
  flex: none;
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
.ghost:hover {
  background: var(--surface-sunken);
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
.primary:hover:not(:disabled) {
  background: var(--accent-hover);
}
</style>
