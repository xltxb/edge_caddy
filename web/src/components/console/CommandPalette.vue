<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useNodesStore } from '@/stores/nodes'
import { suggest, type Suggestion } from '@/utils/palette'

/**
 * ⌘K 命令面板。
 *
 * 执行节点操作时走 nodes store 的 runOp，与节点页的行内按钮**同一条路径**——
 * 面板自己发请求的话，忙碌态和错误提示会跟按钮分叉。
 */
const router = useRouter()
const nodes = useNodesStore()

const open = ref(false)
const query = ref('')
const cursor = ref(0)
const inputEl = ref<HTMLInputElement | null>(null)

const items = computed<Suggestion[]>(() =>
  suggest(
    query.value,
    nodes.nodes.map((n) => ({ id: n.id, city: n.city, ip: n.ip, line: n.line })),
  ),
)

// 输入变了就把游标收回顶部：否则筛完只剩两条，游标还停在第 7 条上，
// 一按 Enter 什么都不会发生。
watch(query, () => (cursor.value = 0))

function onKey(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    open.value = true
    query.value = ''
    cursor.value = 0
    requestAnimationFrame(() => inputEl.value?.focus())
  }
}
onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => window.removeEventListener('keydown', onKey))

function onInputKey(e: KeyboardEvent) {
  const n = items.value.length
  switch (e.key) {
    case 'Escape':
      open.value = false
      return
    case 'ArrowDown':
      e.preventDefault()
      if (n) cursor.value = (cursor.value + 1) % n
      return
    case 'ArrowUp':
      e.preventDefault()
      if (n) cursor.value = (cursor.value - 1 + n) % n
      return
    case 'Enter':
      e.preventDefault()
      void run(items.value[cursor.value])
      return
  }
}

async function run(s: Suggestion | undefined) {
  if (!s) return
  open.value = false
  if (s.kind === 'nav') {
    await router.push(s.to)
    return
  }
  if (s.verb === 'drain') {
    // 面板正是最容易手滑的入口，不能因为「敲命令的人知道自己在做什么」就跳过确认
    nodes.askDrain(s.node)
    return
  }
  await nodes.runOp(s.verb, s.node)
}
</script>

<template>
  <div v-if="open" class="mask" @click.self="open = false">
    <div class="panel" role="dialog" aria-label="命令面板">
      <input
        ref="inputEl"
        v-model="query"
        class="q"
        placeholder="输入命令：push / probe / drain + 节点名、城市、IP，或页面名"
        aria-label="命令"
        @keydown="onInputKey"
      />
      <ul v-if="items.length" class="list">
        <li
          v-for="(s, i) in items"
          :key="s.kind === 'op' ? `${s.verb}:${s.node}` : s.to"
          :class="{ on: i === cursor }"
          :data-destructive="s.destructive ? 'true' : 'false'"
          @click="run(s)"
        >
          <span class="lb">{{ s.label }}</span>
          <span v-if="s.destructive" class="warn">破坏性</span>
        </li>
      </ul>
      <div v-else class="none">没有匹配的命令</div>
    </div>
  </div>
</template>

<style scoped>
.mask { position: fixed; inset: 0; background: rgba(0, 0, 0, .35); display: flex; justify-content: center; padding-top: 12vh; z-index: 50; }
.panel { width: min(560px, 92vw); background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; box-shadow: 0 18px 50px rgba(0, 0, 0, .22); overflow: hidden; height: max-content; }
.q { width: 100%; box-sizing: border-box; border: 0; border-bottom: 1px solid var(--border-subtle); padding: 14px 16px; font-size: 14px; background: transparent; color: var(--text-strong); outline: none; }
.list { list-style: none; margin: 0; padding: 6px; max-height: 46vh; overflow-y: auto; }
.list li { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 8px 11px; border-radius: 9px; cursor: pointer; font-size: 13px; color: var(--text-body); }
.list li.on { background: var(--accent-subtle); color: var(--accent-text); font-weight: 600; }
.warn { font-size: 10px; padding: 1px 6px; border-radius: 999px; background: var(--danger-subtle, #fee); color: var(--danger-text); font-weight: 600; }
.none { padding: 16px; font-size: 13px; color: var(--text-muted); }
</style>
