<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { errorText } from '@/api/http'
import { useRoute, useRouter } from 'vue-router'
import DeployModal from '@/components/workbench/DeployModal.vue'
import FieldList from '@/components/workbench/FieldList.vue'
import JsonDiff from '@/components/workbench/JsonDiff.vue'
import ResourceTree from '@/components/workbench/ResourceTree.vue'
import { useConfigStore } from '@/stores/config'
import { useDeployStore } from '@/stores/deploy'
import { useUiStore } from '@/stores/ui'
import { fieldsFor } from '@/workbench/fields'
import { readableFor } from '@/workbench/readable'

const route = useRoute()
const router = useRouter()
const config = useConfigStore()
const deploy = useDeployStore()
const ui = useUiStore()

const modalOpen = ref(false)

onMounted(async () => {
  if (!config.routes.length) void config.fetchAll().catch(() => {})
  // 刷新时如果有一次下发还在进行，把它接回来 —— 下发在主控侧照常跑，
  // 前端不接的话，人会以为它消失了。
  if (await deploy.resume()) modalOpen.value = true
})

/**
 * 选中的资源。URL 是唯一真相，刷新与外部跳转（如「在工作台编辑」）都靠它。
 *
 * **不要手动 encode/decode** —— vue-router 已经负责参数的编解码。自己再编一次
 * 会得到 `route%253A…` 这种双重编码的地址，靠「两次编码配两次解码」侥幸能跑，
 * 但 URL 不可读，复制出去也不对。
 */
const selected = computed(() => {
  const fromUrl = route.params.key
  const key = Array.isArray(fromUrl) ? fromUrl[0] : fromUrl
  return key || config.tree[0]?.key || ''
})

function select(key: string): void {
  void router.replace({ name: 'workbench', params: { key } })
}

/**
 * 签名规则的**共享密钥不在工作台**。
 *
 * 它不进草稿（草稿由 `GET /drafts` 全局回显），走 `PUT /rules/:id` 的顶层
 * `secret` 直写。不说这一句的话，人在这张表单上找不到密钥，只会当成「还没做」。
 */
const isServiceSecret = computed(
  () => (effective.value as { type?: string } | undefined)?.type === 'service_secret',
)

const live = computed(() => config.live(selected.value))
const effective = computed(() => config.effective(selected.value))
const patch = computed(() => config.patches[selected.value])
const specs = computed(() =>
  effective.value ? fieldsFor(selected.value, effective.value) : [],
)
const domains = computed(() => config.routes.map((r) => r.domain))

/**
 * 右栏是**可读表示**，不是将要下发的字节（ADR-0007）。
 * before 用基线渲染、after 用有效值渲染，两边同一个渲染器，所以这份 diff
 * 忠实反映「我改了什么」——但它不能证明「会下发什么」，那只在确认弹层里成立。
 */
const readableBefore = computed(() =>
  live.value ? readableFor(selected.value, live.value) : '',
)
const readableAfter = computed(() =>
  effective.value ? readableFor(selected.value, effective.value) : '',
)

const serverErrors = computed(() => deploy.fieldErrors[selected.value])

/** 本地校验有没有拦下什么 —— 有就禁用下发按钮，不必等后端。 */
const localInvalid = computed(() =>
  config.dirtyKeys.some((key) => {
    const v = config.effective(key)
    if (!v) return false
    return fieldsFor(key, v).some((s) => (s.validate ? s.validate(v as never) !== null : false))
  }),
)

const isNew = computed(() => (live.value?.version ?? 1) === 0)

function onChange(path: string, v: unknown): void {
  config.setField(selected.value, path, v)
}

async function openDeploy(): Promise<void> {
  modalOpen.value = true
  try {
    await deploy.runPreview(config.dirtyKeys)
  } catch (e) {
    modalOpen.value = false
    ui.toast('warn', '预览失败', errorText(e, ''))
  }
}

function closeDeploy(): void {
  modalOpen.value = false
  if (deploy.phase === 'done') {
    config.commit(deploy.current?.resKeys ?? [])
    void config.fetchAll().catch(() => {})
  }
  deploy.reset()
}

function labelOf(key: string): string {
  return config.tree.find((t) => t.key === key)?.label ?? key
}

// 切换资源时把上一轮的校验红框清掉，否则会落在一个不相干的表单上
watch(selected, () => {
  if (deploy.phase === 'idle') deploy.reset()
})
</script>

<template>
  <div class="wb">
    <aside class="col tree-col">
      <ResourceTree :items="config.tree" :selected="selected" @select="select" />
    </aside>

    <section class="col form-col">
      <header class="col-head">
        <div class="col-title">{{ labelOf(selected) }}</div>
        <span v-if="isNew" class="badge new">尚未下发到任何节点</span>
        <span v-else-if="config.changesOf(selected)" class="badge dirty">
          {{ config.changesOf(selected) }} 处未下发改动
        </span>
        <button
          v-if="config.changesOf(selected)"
          class="mini"
          type="button"
          @click="config.revert(selected)"
        >
          放弃改动
        </button>
      </header>

      <!-- 部分资源没取到时，能用的先用起来，但要说清缺了什么 -->
      <p v-if="config.failedParts.length && config.failedParts.length < 5" class="partial">
        {{ config.failedParts.join('、') }} 没有加载成功，这几类资源暂时不可编辑。
      </p>

      <div v-if="config.loading && !effective" class="hint">正在加载配置…</div>
      <div v-else-if="config.error" class="hint error">
        {{ config.error }}
        <button class="mini" type="button" @click="config.fetchAll()">重试</button>
      </div>
      <div v-else-if="!effective" class="hint">还没有可编辑的资源。</div>
      <div v-else class="form">
        <p v-if="config.updated[selected]" class="who">
          最近由 {{ config.updated[selected]!.by }} 修改 · 草稿全局可见
        </p>
        <p v-if="isServiceSecret" class="elsewhere">
          共享密钥不在这里改：它只写入不回显，也不进草稿。到
          <RouterLink to="/acl">访问控制</RouterLink> 页设置，那里是直写立即生效的。
        </p>
        <FieldList
          :specs="specs"
          :value="effective"
          :live="live!"
          :patch="patch"
          :domain-choices="domains"
          :server-errors="serverErrors"
          @change="onChange"
        />
      </div>
    </section>

    <section class="col repr-col">
      <header class="col-head">
        <!--
          术语按 CONTEXT.md：这里叫「可读表示」。
          不要叫「预览 JSON」或「Caddy JSON 预览」—— 那会让人以为它可下发。
        -->
        <div class="col-title">可读表示</div>
        <!--
          与弹层里的权威 diff 长得不一样是**正常的**，两者回答的是不同的问题。
          尤其服务密钥这类规则：密钥不进 Caddy 配置（Admin API 能读回整份运行
          配置，放进去等于摆在一个可读接口后面），所以真实渲染里只有委托路径，
          没有 spec 字段。不说清楚的话，人并排一看会以为其中一份是错的。
        -->
        <span
          class="col-note"
          title="这一栏回答「我改了什么」；确认弹层里的权威 diff 回答「将要下发什么」。两者由不同的渲染器产出，长得不一样是正常的——例如服务密钥不会出现在下发内容里。"
        >
          我改了什么 <span class="info">ⓘ</span>
        </span>
      </header>
      <div class="repr">
        <JsonDiff :before="readableBefore" :after="readableAfter" />
      </div>
    </section>

    <footer class="bar">
      <div class="bar-info">
        <b class="count">{{ config.totalChanges }}</b>
        <span class="unit">处未下发改动</span>
        <span v-if="config.dirtyKeys.length" class="keys">
          · {{ config.dirtyKeys.map(labelOf).join('、') }}
        </span>
      </div>
      <p v-if="localInvalid" class="bar-warn">有字段填写不合法，请先修正</p>
      <button
        class="deploy"
        type="button"
        :disabled="!config.totalChanges || localInvalid"
        @click="openDeploy"
      >
        校验并下发 →
      </button>
    </footer>

    <DeployModal
      v-if="modalOpen"
      :res-keys="config.dirtyKeys"
      :label-of="labelOf"
      @close="closeDeploy"
      @confirmed="() => {}"
    />
  </div>
</template>

<style scoped>
.wb {
  display: grid;
  grid-template-columns: 232px minmax(320px, 1fr) minmax(360px, 1.1fr);
  gap: 0;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  background: var(--surface-card);
  box-shadow: var(--shadow-xs);
  overflow: hidden;
  min-height: 60vh;
}
.col {
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.tree-col {
  border-right: 1px solid var(--border-subtle);
  background: var(--surface-card);
}
.form-col {
  border-right: 1px solid var(--border-subtle);
}
.repr-col {
  background: var(--surface-sunken);
}
.col-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--border-subtle);
  min-height: 45px;
}
.col-title {
  font-size: var(--fs-sm);
  font-weight: var(--weight-bold);
  color: var(--text-strong);
  font-family: var(--font-mono);
}
.col-note {
  margin-left: auto;
  font-size: var(--fs-micro);
  color: var(--text-faint);
  cursor: help;
}
.col-note .info:hover {
  color: var(--accent-text);
}
.badge {
  font-size: var(--fs-micro);
  font-family: var(--font-mono);
  padding: 1px 8px;
  border-radius: var(--radius-full);
}
.badge.dirty {
  background: var(--accent-subtle);
  color: var(--accent-text);
}
.badge.new {
  background: var(--success-subtle);
  color: var(--success-text);
}
.mini {
  margin-left: auto;
  padding: 3px 9px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-muted);
  font-size: var(--fs-micro);
  cursor: pointer;
}
.mini:hover {
  background: var(--surface-sunken);
  color: var(--text-strong);
}
.form {
  padding: var(--space-4);
  overflow-y: auto;
}
.partial {
  margin: var(--space-3) var(--space-4) 0;
  padding: 7px 10px;
  border-radius: var(--radius-sm);
  background: var(--warning-subtle);
  color: var(--warning-text);
  font-size: var(--fs-2xs);
  line-height: 1.6;
}
.elsewhere {
  margin: 0;
  padding: 8px 10px;
  border-left: 2px solid var(--accent);
  background: var(--surface-sunken, var(--bg-subtle));
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.7;
}
.elsewhere a {
  color: var(--accent);
}
.who {
  margin: 0 0 var(--space-3);
  font-size: var(--fs-micro);
  color: var(--text-faint);
}
.repr {
  overflow: auto;
  padding-bottom: var(--space-3);
}
.hint {
  padding: 40px var(--space-4);
  text-align: center;
  color: var(--text-muted);
  font-size: var(--fs-sm);
}
.hint.error {
  color: var(--danger-text);
}
.bar {
  grid-column: 1 / -1;
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-4);
  border-top: 1px solid var(--border-subtle);
  background: var(--surface-card);
  position: sticky;
  bottom: 0;
}
.bar-info {
  display: flex;
  align-items: baseline;
  gap: var(--space-1-5);
  min-width: 0;
}
.count {
  font-family: var(--font-mono);
  font-size: var(--fs-base);
  color: var(--text-strong);
}
.unit {
  font-size: var(--fs-xs);
  color: var(--text-muted);
}
.keys {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  color: var(--text-faint);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bar-warn {
  margin: 0 0 0 auto;
  font-size: var(--fs-2xs);
  color: var(--danger-text);
}
.deploy {
  margin-left: auto;
  padding: 7px 16px;
  border: 1px solid var(--accent);
  border-radius: var(--radius-sm);
  background: var(--accent);
  color: var(--text-on-accent);
  font-size: var(--fs-xs);
  font-weight: var(--weight-semibold);
  cursor: pointer;
  transition: var(--transition-colors);
}
.bar-warn ~ .deploy {
  margin-left: var(--space-3);
}
.deploy:hover:not(:disabled) {
  background: var(--accent-hover);
  box-shadow: var(--glow-sm);
}
</style>
