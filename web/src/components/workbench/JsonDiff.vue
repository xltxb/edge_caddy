<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { diffLines, foldUnchanged, toLines, type DiffBlock } from '@/utils/diff'

const props = withDefaults(
  defineProps<{ before: string; after: string; context?: number }>(),
  { context: 3 },
)

const expanded = ref<Set<number>>(new Set())

const blocks = computed<DiffBlock[]>(() =>
  foldUnchanged(diffLines(toLines(props.before), toLines(props.after)), props.context),
)

// 内容一换就把展开状态清掉——否则第 3 块的展开会落到一段完全无关的行上
watch(
  () => [props.before, props.after],
  () => expanded.value = new Set(),
)

const isOpen = (i: number) => expanded.value.has(i)

function toggle(i: number): void {
  const next = new Set(expanded.value)
  if (next.has(i)) next.delete(i)
  else next.add(i)
  expanded.value = next
}

const total = computed(() =>
  blocks.value.reduce((n, b) => n + b.lines.filter((l) => l.op !== 'same').length, 0),
)
</script>

<template>
  <div class="diff">
    <div v-if="total === 0" class="clean">与基线一致，没有需要下发的差异。</div>

    <template v-for="(b, i) in blocks" :key="i">
      <button v-if="b.kind === 'fold' && !isOpen(i)" type="button" class="fold" @click="toggle(i)">
        ⋯ {{ b.count }} 行未变更，点击展开
      </button>
      <template v-else>
        <button v-if="b.kind === 'fold'" type="button" class="fold open" @click="toggle(i)">
          收起这 {{ b.count }} 行
        </button>
        <div v-for="(l, j) in b.lines" :key="j" class="line" :class="l.op">
          <!--
            前后各一列行号。合成一列的话，删除行显示旧行号、新增行显示新行号，
            同一个数字会连着出现两三次，读起来像行号错乱。
          -->
          <span class="no">{{ l.beforeNo ?? '' }}</span>
          <span class="no">{{ l.afterNo ?? '' }}</span>
          <span class="sign">{{ l.op === 'add' ? '+' : l.op === 'del' ? '-' : ' ' }}</span>
          <span class="text">{{ l.text }}</span>
        </div>
      </template>
    </template>
  </div>
</template>

<style scoped>
.diff {
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  line-height: 1.65;
  overflow-x: auto;
}
.clean {
  padding: 28px var(--space-4);
  color: var(--text-muted);
  font-family: var(--font-sans);
  font-size: var(--fs-xs);
  text-align: center;
}
.line {
  display: flex;
  gap: var(--space-2);
  padding: 0 var(--space-3);
  white-space: pre;
}
.line.add {
  background: var(--success-subtle);
}
.line.del {
  background: var(--danger-subtle);
}
.no {
  width: 2.4em;
  text-align: right;
  color: var(--text-faint);
  flex: none;
  user-select: none;
}
.sign {
  width: 1ch;
  flex: none;
  user-select: none;
  color: var(--text-faint);
}
.line.add .sign {
  color: var(--success-text);
}
.line.del .sign {
  color: var(--danger-text);
}
.text {
  color: var(--text-body);
}
.fold {
  display: block;
  width: 100%;
  padding: 3px var(--space-3);
  border: 0;
  border-top: 1px solid var(--border-subtle);
  border-bottom: 1px solid var(--border-subtle);
  background: var(--surface-sunken);
  color: var(--text-muted);
  font-family: inherit;
  font-size: var(--fs-micro);
  text-align: left;
  cursor: pointer;
}
.fold:hover {
  color: var(--accent-text);
}
</style>
