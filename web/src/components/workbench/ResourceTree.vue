<script setup lang="ts">
import { computed } from 'vue'
import type { ResourceItem } from '@/stores/config'

const props = defineProps<{ items: ResourceItem[]; selected: string }>()
defineEmits<{ (e: 'select', key: string): void }>()

/*
 * 分组顺序是固定的，**但名单外的分组不能被丢掉**。
 *
 * 这里原先只 map 那三个名字，于是 store 新加的「没有底子的草稿」整组被静默
 * 扔掉 —— 我刚给 store 加完那一组，树上什么也没出现，而且不报错。
 *
 * **这跟我在 store 里防的是同一件事**：顶栏的「N 处变更」把它数进去了，
 * 而列表里一行都没有。我在数据层把它显示出来，又在渲染层把它藏了回去。
 * 白名单式的渲染就是这么丢东西的：它不报错，它什么也不做。
 */
const ORDER = ['反代路由', '访问规则', '全局策略']

const groups = computed(() => {
  const seen = new Map<string, ResourceItem[]>()
  for (const it of props.items) {
    const arr = seen.get(it.group)
    if (arr) arr.push(it)
    else seen.set(it.group, [it])
  }
  const rank = (g: string) => {
    const i = ORDER.indexOf(g)
    return i === -1 ? ORDER.length : i // 名单外的排在最后，而不是消失
  }
  return [...seen.entries()]
    .map(([group, items]) => ({ group, items }))
    .sort((a, b) => rank(a.group) - rank(b.group))
})
</script>

<template>
  <nav class="tree">
    <template v-for="g in groups" :key="g.group">
      <div class="group">{{ g.group }}</div>
      <button
        v-for="it in g.items"
        :key="it.key"
        type="button"
        class="item"
        :class="{ on: selected === it.key, orphan: it.orphan }"
        :disabled="it.orphan"
        :title="
          it.orphan
            ? '这份草稿底下没有资源，打不开也下发不了。草稿是在已有资源上的改动，' +
              '要新建请先把资源本身建出来。'
            : undefined
        "
        @click="!it.orphan && $emit('select', it.key)"
      >
        <span class="label">{{ it.label }}</span>
        <!-- 蓝点 = 有未下发改动。它必须与「待下发」计数同源，否则就是在撒谎。 -->
        <span v-if="it.dirty" class="dot" :title="`${it.changes} 处未下发改动`" />
        <span v-if="it.isNew" class="new">新</span>
        <!--
          **不能只把它禁用了事。** 一个点不动、也不说为什么的条目，人只会以为
          界面坏了 —— 而这一条恰恰有明确的处置办法（先把资源建出来）。
        -->
        <span v-if="it.orphan" class="orphan-tag">没有底子</span>
      </button>
    </template>
  </nav>
</template>

<style scoped>
/*
 * 孤儿草稿：显示、但点不动。灰掉而不是藏起来 —— 顶栏的「N 处变更」把它数
 * 进去了，藏起来就等于让那个数字指向一个看不见的东西。
 */
.item.orphan {
  cursor: default;
  opacity: 0.75;
}
.orphan-tag {
  flex: none;
  padding: 1px 6px;
  border-radius: var(--radius-xs, 3px);
  background: var(--warning-bg, var(--surface-sunken));
  color: var(--warning-text);
  font-size: var(--fs-micro);
  white-space: nowrap;
}
.tree {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: var(--space-3);
  overflow-y: auto;
}
.group {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  letter-spacing: var(--tracking-caps);
  text-transform: uppercase;
  color: var(--text-muted);
  font-weight: var(--weight-semibold);
  padding: var(--space-3) var(--space-2) var(--space-1);
}
.group:first-child {
  padding-top: 0;
}
.item {
  display: flex;
  align-items: center;
  gap: var(--space-1-5);
  width: 100%;
  padding: 6px 9px;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-body);
  font-size: var(--fs-xs);
  font-family: var(--font-mono);
  text-align: left;
  cursor: pointer;
  transition: var(--transition-colors);
}
.item:hover {
  background: var(--surface-sunken);
}
.item.on {
  background: var(--accent-subtle);
  color: var(--accent-text);
  font-weight: var(--weight-semibold);
}
.label {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--accent);
  flex: none;
}
.new {
  font-size: var(--fs-micro);
  padding: 0 5px;
  border-radius: var(--radius-full);
  background: var(--success-subtle);
  color: var(--success-text);
  flex: none;
}
</style>
