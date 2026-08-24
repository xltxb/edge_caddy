<script setup lang="ts">
import { computed } from 'vue'
import type { RequestFilter } from '@/api/types'

/**
 * 请求特征的行编辑器。
 *
 * ## 下拉的选项照后端报的表渲染，不在这里写死
 *
 * `field → 允许的 op` 由 `GET /rules` 的 `filter_fields` 给（契约 §6.2），
 * 与后端的校验**共用同一张表**。抄一份的代价是两边在某次改动时分叉，
 * 而那个分叉两个方向不对称：
 *
 * - 给出一个后端会拒的选项 —— 人配完被拒，还算看得见。
 * - 藏起一个后端接受的 —— 从界面上完全看不出来。
 *
 * 所以宁可在表没到时**什么都不渲染并说出来**，也不退回一份本地默认表。
 *
 * ## `query` 那一档只有 equals
 *
 * 那不是我们的选择：Caddy 的 query 匹配器只比精确值。这一点不写死在这里 ——
 * 它在后端那张表里，这个组件只是照着渲染。**特例不会提醒下一个人**，
 * 而一张表会。
 */
const props = defineProps<{
  id: string
  modelValue: RequestFilter[] | undefined
  disabled?: boolean
  /** field → 允许的 op。`null` = 主控还没报（太旧）。 */
  fields: Record<string, string[]> | null
  /** 哪些 field 还要指明「看哪一个」。同样从表里读，不再判一次 field 名。 */
  needsName: Record<string, boolean> | null
}>()

const emit = defineEmits<{ (e: 'update:modelValue', v: RequestFilter[]): void }>()

const rows = computed<RequestFilter[]>(() => props.modelValue ?? [])
const fieldNames = computed(() => Object.keys(props.fields ?? {}))

/** 这一行的 field 允许哪些 op。field 不在表里时给空 —— 那一行的 op 下拉是空的。 */
function opsFor(field: string): string[] {
  return props.fields?.[field] ?? []
}

function needsName(field: string): boolean {
  return props.needsName?.[field] === true
}

function patch(i: number, key: keyof RequestFilter, v: string): void {
  const next = rows.value.map((r, j) => (j === i ? { ...r, [key]: v } : { ...r }))
  const row = next[i]
  /*
   * **换了 field 之后，原来的 op 可能不在新 field 的允许集里。**
   *
   * 例：把 `path`（有 contains）换成 `query`（只有 equals）—— 留着 contains
   * 会得到一个后端一定会拒的组合，而下拉上显示的是一个**看起来选中了**的值。
   * 就地改成新集合的第一个，让界面上永远只出现合法组合。
   */
  if (key === 'field' && row) {
    const ops = opsFor(row.field)
    if (ops.length && !ops.includes(row.op)) row.op = ops[0]!
    // 新 field 不需要 name 了就把它清掉，别留一个不再显示、却会被发出去的值
    if (!needsName(row.field)) delete row.name
  }
  emit('update:modelValue', next)
}

function add(): void {
  const first = fieldNames.value[0] ?? ''
  emit('update:modelValue', [
    ...rows.value.map((r) => ({ ...r })),
    { field: first, op: opsFor(first)[0] ?? '', value: '' },
  ])
}

function remove(i: number): void {
  emit(
    'update:modelValue',
    rows.value.filter((_, j) => j !== i).map((r) => ({ ...r })),
  )
}
</script>

<template>
  <div class="filters">
    <!--
      主控还没报那张表时**不渲染任何下拉**，并说出原因。

      退回一份本地默认表的话，人配出来的组合可能是这个主控不接受的 ——
      而界面上看不出任何异常。说一句「不知道有哪些」比给一份可能是错的清单好。
    -->
    <div v-if="!fields" class="empty">
      这个主控还没报出可用的匹配字段（旧版本），这里暂时编辑不了。
    </div>

    <template v-else>
      <div v-if="!rows.length" class="empty">
        还没有特征。加一条 —— 在这之前这条规则谁也拦不到。
      </div>

      <div v-for="(r, i) in rows" :key="i" class="row">
        <select
          :id="i === 0 ? id : undefined"
          class="sel"
          :disabled="disabled"
          :value="r.field"
          aria-label="匹配字段"
          @change="patch(i, 'field', ($event.target as HTMLSelectElement).value)"
        >
          <option v-for="f in fieldNames" :key="f" :value="f">{{ f }}</option>
        </select>

        <!-- header / query 要指明看哪一个。需不需要由后端那张表说了算 -->
        <input
          v-if="needsName(r.field)"
          class="text name"
          :disabled="disabled"
          :value="r.name ?? ''"
          :placeholder="r.field === 'query' ? '参数名' : '头名'"
          aria-label="看哪一个"
          @input="patch(i, 'name', ($event.target as HTMLInputElement).value)"
        />

        <select
          class="sel op"
          :disabled="disabled"
          :value="r.op"
          aria-label="比较方式"
          @change="patch(i, 'op', ($event.target as HTMLSelectElement).value)"
        >
          <option v-for="o in opsFor(r.field)" :key="o" :value="o">{{ o }}</option>
        </select>

        <input
          class="text val"
          :disabled="disabled"
          :value="r.value"
          placeholder="要匹配的值"
          aria-label="要匹配的值"
          @input="patch(i, 'value', ($event.target as HTMLInputElement).value)"
        />

        <button class="mini" type="button" :disabled="disabled" @click="remove(i)">删除</button>
      </div>

      <button class="mini add" type="button" :disabled="disabled" @click="add">添加特征</button>
    </template>
  </div>
</template>

<style scoped>
.filters {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  align-items: stretch;
}
.row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2);
}
.sel,
.text {
  padding: 5px 8px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-card);
  color: var(--text-strong);
  font-size: var(--fs-2xs);
  min-width: 0;
}
.sel {
  font-family: var(--font-mono);
}
.op {
  min-width: 110px;
}
.name {
  width: 130px;
}
.val {
  /*
   * **basis 要小。** 工作台中栏只有 ~450px，而一行要放下三个控件加一个按钮。
   * 给 160px 的话，第一行刚好放不下 —— 「删除」被挤到下一行，
   * 每条特征占两行高，四条就把这一格撑满了。
   *
   * 撑不下时它自己会伸展（flex-grow 1），所以窄的 basis 不损失什么。
   */
  flex: 1 1 90px;
  font-family: var(--font-mono);
}
/* 按钮不参与伸缩 —— 它被压扁的话「删除」两个字会换行 */
.row > button {
  flex: 0 0 auto;
}
.add {
  align-self: flex-start;
}
.empty {
  padding: var(--space-3);
  border: 1px dashed var(--border-default);
  border-radius: var(--radius-sm);
  font-size: var(--fs-2xs);
  color: var(--text-muted);
}
</style>
