<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { isRunnable, suggest, type Suggestion } from '@/palette/suggest'
import { useConfigStore } from '@/stores/config'
import { useNodesStore } from '@/stores/nodes'
import { useOverviewStore } from '@/stores/overview'
import { useUiStore } from '@/stores/ui'

/**
 * 命令面板（⌘K）。全局挂载。
 *
 * 命令执行**复用各 store 的 action**，不旁路 —— 从这里 pause 一个节点，
 * 与在节点页点「暂停解析」必须走同一条路径，否则两处的行为迟早会分叉，
 * 而分叉的那一半没人测。
 */
const router = useRouter()
const ui = useUiStore()
const nodes = useNodesStore()
const config = useConfigStore()
const overview = useOverviewStore()

const query = ref('')
const cursor = ref(0)
const input = ref<HTMLInputElement | null>(null)

const items = computed<Suggestion[]>(() =>
  suggest({
    query: query.value,
    nodes: nodes.items,
    domains: config.routes.map((r) => ({ domain: r.domain, upstream: r.upstream })),
    baseline: overview.baseline || 'cfg-…',
  }),
)

watch(query, () => (cursor.value = 0))
watch(
  () => ui.paletteOpen,
  async (open) => {
    if (!open) return
    query.value = ''
    cursor.value = 0
    await nextTick()
    input.value?.focus()
  },
)

function move(delta: number): void {
  const n = items.value.length
  if (!n) return
  cursor.value = (cursor.value + delta + n) % n
}

async function run(): Promise<void> {
  const s = items.value[cursor.value]
  if (!isRunnable(s)) return
  ui.paletteOpen = false

  const id = s!.nodeId
  switch (s!.act) {
    case 'focus':
      if (id) await router.push({ name: 'nodes', query: { open: id } })
      break
    case 'push':
      if (id) {
        try {
          const r = await nodes.pushOne(id)
          ui.toast('ok', `已向 ${id} 重推基线`, r.cfg_version)
        } catch (e) {
          ui.toast('warn', '重推失败', e instanceof Error ? e.message : '')
        }
      }
      break
    case 'pause':
    case 'resume':
      if (id) {
        const enabled = s!.act === 'resume'
        try {
          await nodes.toggleDns(id, enabled)
          ui.toast(enabled ? 'ok' : 'warn', `${id} ${enabled ? '已恢复解析' : '已暂停解析'}`)
        } catch (e) {
          ui.toast('warn', '操作失败', e instanceof Error ? e.message : '')
        }
      }
      break
    case 'goto':
      if (s!.resKey) {
        await router.push({ name: 'workbench', params: { key: s!.resKey } })
      }
      break
  }
}

const KIND_COLOR: Record<string, string> = {
  节点: 'var(--accent-text)',
  命令: 'var(--text-faint)',
  执行: 'var(--success-text)',
  域名: 'var(--cyan-600)',
  错误: 'var(--danger-text)',
}
</script>

<template>
  <div v-if="ui.paletteOpen" class="mask" @click.self="ui.paletteOpen = false">
    <div class="palette" role="dialog" aria-modal="true" aria-label="命令面板">
      <input
        ref="input"
        v-model="query"
        class="input"
        type="text"
        placeholder="push cfg-… to <node> · pause/resume <node> · logs <node> · 或搜节点、城市、IP、线路、域名"
        autocomplete="off"
        spellcheck="false"
        @keydown.down.prevent="move(1)"
        @keydown.up.prevent="move(-1)"
        @keydown.enter.prevent="run"
        @keydown.esc.prevent="ui.paletteOpen = false"
      />

      <ul v-if="items.length" class="list">
        <li
          v-for="(s, i) in items"
          :key="`${s.kind}-${s.label}-${i}`"
          class="item"
          :class="{ on: i === cursor, dim: !isRunnable(s) }"
          @mouseenter="cursor = i"
          @click="run"
        >
          <span class="kind" :style="{ color: KIND_COLOR[s.kind] }">{{ s.kind }}</span>
          <span class="label">{{ s.label }}</span>
          <span class="hint">{{ s.hint }}</span>
        </li>
      </ul>
      <div v-else class="empty">没有匹配的节点、域名或命令。</div>

      <div class="foot">
        <span><kbd>↑</kbd><kbd>↓</kbd> 选择</span>
        <span><kbd>Enter</kbd> 执行</span>
        <span><kbd>Esc</kbd> 关闭</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mask {
  position: fixed;
  inset: 0;
  background: var(--surface-overlay);
  z-index: var(--z-overlay);
  display: flex;
  justify-content: center;
  padding-top: 14vh;
}
.palette {
  width: min(680px, calc(100% - 2 * var(--space-6)));
  align-self: flex-start;
  background: var(--surface-raised);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xl);
  overflow: hidden;
}
.input {
  width: 100%;
  padding: 14px var(--space-4);
  border: 0;
  border-bottom: 1px solid var(--border-subtle);
  background: transparent;
  color: var(--text-strong);
  font-family: var(--font-mono);
  font-size: var(--fs-sm);
}
.input:focus {
  outline: none;
}
.input::placeholder {
  color: var(--text-faint);
  font-size: var(--fs-2xs);
}
.list {
  list-style: none;
  margin: 0;
  padding: var(--space-1-5);
  max-height: 46vh;
  overflow-y: auto;
}
.item {
  display: flex;
  align-items: baseline;
  gap: var(--space-3);
  padding: 7px 10px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  font-size: var(--fs-xs);
}
.item.on {
  background: var(--accent-subtle);
}
.item.dim {
  cursor: default;
}
.kind {
  font-family: var(--font-mono);
  font-size: var(--fs-micro);
  flex: none;
  width: 2.6em;
}
.label {
  font-family: var(--font-mono);
  color: var(--text-strong);
}
.item.dim .label {
  color: var(--text-muted);
}
.hint {
  margin-left: auto;
  font-size: var(--fs-micro);
  color: var(--text-faint);
  text-align: right;
}
.empty {
  padding: 28px var(--space-4);
  text-align: center;
  color: var(--text-muted);
  font-size: var(--fs-xs);
}
.foot {
  display: flex;
  gap: var(--space-4);
  padding: 8px var(--space-4);
  border-top: 1px solid var(--border-subtle);
  font-size: var(--fs-micro);
  color: var(--text-faint);
}
kbd {
  font-family: var(--font-mono);
  border: 1px solid var(--border-default);
  border-radius: 3px;
  padding: 0 4px;
  margin-right: 3px;
}
</style>
