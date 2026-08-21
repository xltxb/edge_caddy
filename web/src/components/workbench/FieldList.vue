<script setup lang="ts">
import { computed } from 'vue'
import VAreaField from './VAreaField.vue'
import VChipsField from './VChipsField.vue'
import VSegField from './VSegField.vue'
import VSwitchField from './VSwitchField.vue'
import VTextField from './VTextField.vue'
import type { FieldSpec } from '@/workbench/field-spec'
import { getPath, isVisible, resolveHint, resolveUnavailable } from '@/workbench/field-spec'
import { isFieldDirty, type Patch } from '@/workbench/draft'

/**
 * 字段渲染器 —— ADR-0012 的落点。
 *
 * 六套资源布局共用这一个组件：改动着色、错误着色、hint 随值变化，
 * 三种行为在这里各实现一次，而不是在六个手写表单里各实现六次。
 */
const props = defineProps<{
  specs: FieldSpec<never>[]
  /** 有效值 = 基线 + 草稿 */
  value: Record<string, unknown>
  /** 基线值，用于判断某个字段是否被改过 */
  live: Record<string, unknown>
  patch: Patch | undefined
  /** chips 字段的候选项（当前全部域名） */
  domainChoices: string[]
  /** 后端回来的校验错误，按字段路径索引（契约 §0.3 的 field 是点号路径） */
  serverErrors?: Record<string, string>
}>()

const emit = defineEmits<{ (e: 'change', path: string, v: unknown): void }>()

/**
 * 每个字段一个稳定 id，用于把 <label> 和控件关联起来。
 *
 * 不关联的话，屏幕阅读器念不出这个输入框是干什么的，点标签也不会聚焦到控件上。
 * 顺带让 e2e 能用 getByLabel 这种语义选择器，而不是靠位置去猜。
 */
const fieldId = (path: string) => `f-${path.replace(/[^a-zA-Z0-9]/g, '-')}`

interface Row {
  spec: FieldSpec<never>
  group: string
  dirty: boolean
  /** 本地校验 + 后端校验，本地优先（它更即时） */
  error: string | null
  hint: string
  /** 「这件事我们做不到」的原因；非 null 时控件置灰。 */
  unavailable: string | null
  current: unknown
}

const rows = computed<Row[]>(() =>
  props.specs
    .filter((s) => isVisible(s, props.value as never))
    .map((spec) => {
      const local = spec.validate ? spec.validate(props.value as never) : null
      return {
        spec,
        group: spec.group ?? '',
        dirty: isFieldDirty(props.live, props.patch, spec.field),
        error: local ?? props.serverErrors?.[spec.field] ?? null,
        hint: resolveHint(spec, props.value as never),
        unavailable: resolveUnavailable(spec, props.value as never),
        current: getPath(props.value, spec.field),
      }
    }),
)

/** 相邻同组合并成一段，组名只在段首出现一次。 */
const sections = computed(() => {
  const out: { group: string; rows: Row[] }[] = []
  for (const r of rows.value) {
    const last = out[out.length - 1]
    if (last && last.group === r.group) last.rows.push(r)
    else out.push({ group: r.group, rows: [r] })
  }
  return out
})

function switchText(r: Row): string {
  const s = r.spec
  if (s.kind !== 'switch') return ''
  if (r.current === true) {
    return typeof s.onText === 'function' ? s.onText(props.value as never) : s.onText
  }
  return s.offText
}

function switchWarn(r: Row): boolean {
  return r.spec.kind === 'switch' && r.current !== true && r.spec.offWarn === true
}
</script>

<template>
  <div class="list">
    <template v-for="(sec, si) in sections" :key="si">
      <div v-if="sec.group" class="group">{{ sec.group }}</div>

      <div v-for="r in sec.rows" :key="r.spec.field" class="row">
        <label class="label" :for="fieldId(r.spec.field)">
          {{ r.spec.label }}
          <span v-if="r.dirty" class="dot" title="有未下发改动" />
        </label>

        <div class="control">
          <VTextField
            v-if="r.spec.kind === 'text'"
            :id="fieldId(r.spec.field)"
            :model-value="r.current"
            :dirty="r.dirty"
            :invalid="!!r.error"
            :width="r.spec.width"
            :numeric="r.spec.numeric"
            :disabled="!!r.unavailable"
            @update:model-value="(v) => emit('change', r.spec.field, v)"
          />
          <VAreaField
            v-else-if="r.spec.kind === 'area'"
            :id="fieldId(r.spec.field)"
            :model-value="r.current"
            :dirty="r.dirty"
            :invalid="!!r.error"
            :rows="r.spec.rows"
            @update:model-value="(v) => emit('change', r.spec.field, v)"
          />
          <VSegField
            v-else-if="r.spec.kind === 'seg'"
            :model-value="r.current"
            :options="r.spec.options"
            :dirty="r.dirty"
            @update:model-value="(v) => emit('change', r.spec.field, v)"
          />
          <div v-else-if="r.spec.kind === 'switch'" class="switch-row">
            <VSwitchField
              :id="fieldId(r.spec.field)"
              :model-value="r.current"
              :dirty="r.dirty"
              :disabled="!!r.unavailable"
              @update:model-value="(v) => emit('change', r.spec.field, v)"
            />
            <span class="switch-text" :class="{ warn: switchWarn(r) }">{{ switchText(r) }}</span>
          </div>
          <VChipsField
            v-else
            :model-value="r.current"
            :choices="domainChoices"
            :dirty="r.dirty"
            @update:model-value="(v) => emit('change', r.spec.field, v)"
          />

          <!--
            三条说明只出现一条，优先级是 错误 > 做不到 > 提示。
            「做不到」不是校验失败 —— 没人填错任何东西，是组件没有这个能力，
            所以它不用 danger 色；但也不能混进普通 hint 里被读成一句闲话。
          -->
          <p v-if="r.error" class="err">{{ r.error }}</p>
          <p v-else-if="r.unavailable" class="unavail">{{ r.unavailable }}</p>
          <p v-else-if="r.hint" class="hint">{{ r.hint }}</p>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.list {
  display: flex;
  flex-direction: column;
  gap: 15px;
}
.group {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-muted);
  font-weight: var(--weight-semibold);
  padding-top: var(--space-2);
  border-top: 1px solid var(--border-subtle);
}
.group:first-child {
  padding-top: 0;
  border-top: 0;
}
.row {
  display: flex;
  flex-direction: column;
  gap: var(--space-1-5);
}
.label {
  display: flex;
  align-items: center;
  gap: var(--space-1-5);
  font-size: var(--fs-xs);
  color: var(--text-body);
  font-weight: var(--weight-medium);
}
.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--accent);
  flex: none;
}
.control {
  display: flex;
  flex-direction: column;
  gap: var(--space-1-5);
  align-items: flex-start;
}
.switch-row {
  display: flex;
  align-items: center;
  gap: var(--space-2-5, 10px);
}
.switch-text {
  font-size: var(--fs-2xs);
  color: var(--text-faint);
}
.switch-text.warn {
  color: var(--warning-text);
}
.hint {
  margin: 0;
  font-size: var(--fs-2xs);
  color: var(--text-muted);
  line-height: 1.6;
}
.unavail {
  margin: 0;
  padding-left: var(--space-2);
  border-left: 2px solid var(--warning-text);
  font-size: var(--fs-2xs);
  color: var(--warning-text);
  line-height: 1.6;
}
.err {
  margin: 0;
  font-size: var(--fs-2xs);
  color: var(--danger-text);
  line-height: 1.6;
}
</style>
